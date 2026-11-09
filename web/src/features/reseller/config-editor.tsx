/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { useMutation } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { StaticDataTable } from '@/components/data-table'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { FormNavigationGuard } from '@/features/system-settings/components/form-navigation-guard'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'

import {
  resellerPath,
  resellerRequest,
  type Config,
  type ConfigItem,
} from './api'
import { ResellerConfigError } from './config-error'
import { ResellerRatioField } from './config-ratio-field'

const configItemKey = (item: ConfigItem) => `${item.model}:${item.channel_id}`

function isValidResellerRatio(value: string): boolean {
  return (
    value === '' ||
    (/^(0|1|2)(\.[0-9]{1,6})?$/.test(value) &&
      Number(value) > 0 &&
      Number(value) <= 2)
  )
}

export function ResellerConfigEditor({
  userID,
  admin,
  config,
  candidates,
  onSuccess,
  onReload,
  authorization = true,
  consumptionEditable = true,
  onDirtyChange,
}: {
  userID: number
  admin: boolean
  onDirtyChange?: (dirty: boolean) => void
  authorization?: boolean
  consumptionEditable?: boolean
  config: Config
  candidates: ConfigItem[]
  onSuccess: () => void
  onReload: () => Promise<void>
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const [items, setItems] = useState(config.items)
  const [filter, setFilter] = useState('')
  const [saved, setSaved] = useState(false)
  const dirty = !saved && JSON.stringify(items) !== JSON.stringify(config.items)
  useEffect(() => {
    onDirtyChange?.(dirty)
  }, [dirty, onDirtyChange])
  const normalizedItems = items.map((item) => ({
    ...item,
    ratio: item.ratio || '1',
  }))
  const invalidItems = items.filter((item) => !isValidResellerRatio(item.ratio))
  const preview = useMutation({
    meta: { errorToast: false },
    mutationFn: () =>
      resellerRequest<{ affected_customers: number }>(
        `${resellerPath(admin)}/${userID}/config/preview`,
        'post',
        { expected_revision: config.revision, items: normalizedItems }
      ),
  })
  const mutation = useMutation({
    meta: { errorToast: false },
    mutationFn: () =>
      resellerRequest(`${resellerPath(admin)}/${userID}/config`, 'put', {
        expected_revision: config.revision,
        items: normalizedItems,
      }),
    onSuccess: () => {
      setSaved(true)
      onSuccess()
    },
  })
  const affectedCustomers = preview.data?.affected_customers ?? 0
  const originalGrants = new Set(
    config.items
      .filter((item) => item.authorized)
      .map((item) => `${item.model}:${item.channel_id}`)
  )
  const currentGrants = new Set(
    items
      .filter((item) => item.authorized)
      .map((item) => `${item.model}:${item.channel_id}`)
  )
  const grantsChanged =
    originalGrants.size !== currentGrants.size ||
    [...originalGrants].some((key) => !currentGrants.has(key))
  const available = new Set(candidates.map(configItemKey))
  const configured = new Set(config.items.map(configItemKey))
  const drafts = new Map(items.map((item) => [configItemKey(item), item]))
  const busy = mutation.isPending || preview.isPending || preview.isSuccess
  const rows = [
    ...config.items.map((item) => drafts.get(configItemKey(item)) ?? item),
    ...candidates
      .filter((candidate) => !configured.has(configItemKey(candidate)))
      .map(
        (candidate) =>
          drafts.get(configItemKey(candidate)) ?? {
            ...candidate,
            authorized: false,
            ratio: '1',
          }
      ),
  ]
  function update(row: ConfigItem, changed: Partial<ConfigItem>) {
    setSaved(false)
    const key = configItemKey(row)
    setItems((previous) => [
      ...previous.filter((item) => configItemKey(item) !== key),
      { ...row, ...changed },
    ])
  }
  return (
    <div className='space-y-4'>
      <FormNavigationGuard when={dirty} />
      <p className='text-muted-foreground text-sm'>
        {consumptionEditable
          ? t(
              'Ratios must be greater than 0 and at most 2, with up to six decimal places. Missing ratios use 1.'
            )
          : t(
              'Configure authorization here. Consumption ratios are managed in realtime model pricing.'
            )}
      </p>
      <div className='max-w-md space-y-2'>
        <Label htmlFor='config-model-search'>{t('Search models')}</Label>
        <Input
          id='config-model-search'
          value={filter}
          onChange={(event) => setFilter(event.target.value)}
        />
      </div>
      <StaticDataTable
        data={rows.filter((row) => row.model.includes(filter))}
        getRowKey={configItemKey}
        emptyContent={t('No models available')}
        columns={[
          { id: 'model', header: t('Model'), cell: (row) => row.model },
          {
            id: 'channel',
            header: t('Channel'),
            cell: (row) => String(row.channel_id),
          },
          {
            id: 'authorized',
            header: t('Authorized'),
            cell: (row) =>
              authorization ? (
                <Checkbox
                  aria-label={t('Authorize {{model}} channel {{channel}}', {
                    model: row.model,
                    channel: row.channel_id,
                  })}
                  checked={row.authorized}
                  disabled={
                    busy || (!admin && !available.has(configItemKey(row)))
                  }
                  onCheckedChange={(checked) =>
                    update(row, { authorized: checked })
                  }
                />
              ) : (
                '—'
              ),
          },
          {
            id: 'ratio',
            className: consumptionEditable ? undefined : 'hidden',
            cellClassName: consumptionEditable ? undefined : 'hidden',
            header: t('Personal ratio'),
            cell: (row) => (
              <ResellerRatioField
                item={row}
                invalid={!isValidResellerRatio(row.ratio)}
                disabled={
                  !consumptionEditable ||
                  busy ||
                  (!admin && !available.has(configItemKey(row)))
                }
                onChange={(ratio) => update(row, { ratio })}
              />
            ),
          },
          {
            id: 'effective',
            header: t('Effective status'),
            cell: (row) => {
              if (!authorization) return '—'
              if (!admin && !available.has(configItemKey(row))) {
                return t('Parent authorization revoked; configuration retained')
              }
              if (consumptionEditable && !isValidResellerRatio(row.ratio)) {
                return '—'
              }
              return row.authorized ? t('Effective') : t('Not authorized')
            },
          },
        ]}
      />
      {invalidItems.some((item) => !item.model.includes(filter)) && (
        <p role='alert'>{t('Clear the search to see invalid ratios.')}</p>
      )}
      <ResellerConfigError
        error={mutation.error ?? preview.error}
        onReload={onReload}
      />
      <ConfirmDialog
        open={preview.isSuccess}
        onOpenChange={(open) => {
          if (!open) preview.reset()
        }}
        title={t('Confirm authorization changes')}
        desc={t(
          '{{count}} customers will be affected. Revoked configurations are retained and become effective again when authorization is restored.',
          {
            count: affectedCustomers,
            replace: { count: formatNumber(affectedCustomers, locale) },
          }
        )}
        isLoading={mutation.isPending}
        handleConfirm={() => {
          preview.reset()
          mutation.mutate()
        }}
      />
      <Button
        disabled={!dirty || invalidItems.length > 0 || busy}
        onClick={() =>
          admin && authorization && grantsChanged
            ? preview.mutate()
            : mutation.mutate()
        }
      >
        {t('Save changes')}
      </Button>
    </div>
  )
}
