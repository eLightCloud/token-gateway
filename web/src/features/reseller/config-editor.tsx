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

export function ResellerConfigEditor({
  userID,
  admin,
  config,
  candidates,
  onSuccess,
  onReload,
  authorization = true,
  onDirtyChange,
}: {
  userID: number
  admin: boolean
  onDirtyChange?: (dirty: boolean) => void
  authorization?: boolean
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
  const valid = normalizedItems.every(
    (item) =>
      /^(0|1|2)(\.[0-9]{1,6})?$/.test(item.ratio) &&
      Number(item.ratio) > 0 &&
      Number(item.ratio) <= 2
  )
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
  const available = new Set(
    candidates.map((item) => `${item.model}:${item.channel_id}`)
  )
  const rows = [
    ...items,
    ...candidates
      .filter(
        (candidate) =>
          !items.some(
            (item) =>
              item.model === candidate.model &&
              item.channel_id === candidate.channel_id
          )
      )
      .map((item) => ({ ...item, authorized: false, ratio: '1' })),
  ]
  function update(row: ConfigItem, changed: Partial<ConfigItem>) {
    setSaved(false)
    setItems((previous) => [
      ...previous.filter(
        (item) => item.model !== row.model || item.channel_id !== row.channel_id
      ),
      { ...row, ...changed },
    ])
  }
  return (
    <div className='space-y-4'>
      <FormNavigationGuard when={dirty} />
      <p className='text-muted-foreground text-sm'>
        {t(
          'Ratios must be greater than 0 and at most 2, with up to six decimal places. Missing ratios use 1.'
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
        getRowKey={(row) => `${row.model}:${row.channel_id}`}
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
                    mutation.isPending ||
                    (!admin && !available.has(`${row.model}:${row.channel_id}`))
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
            header: t('Personal ratio'),
            cell: (row) => (
              <Input
                aria-label={t('Ratio for {{model}} channel {{channel}}', {
                  model: row.model,
                  channel: row.channel_id,
                })}
                className='w-28'
                inputMode='decimal'
                value={row.ratio}
                disabled={
                  mutation.isPending ||
                  (!admin && !available.has(`${row.model}:${row.channel_id}`))
                }
                onChange={(event) => update(row, { ratio: event.target.value })}
              />
            ),
          },
          {
            id: 'effective',
            header: t('Effective status'),
            cell: (row) => {
              if (!authorization) return '—'
              if (!admin && !available.has(`${row.model}:${row.channel_id}`)) {
                return t('Parent authorization revoked; configuration retained')
              }
              return row.authorized ? t('Effective') : t('Not authorized')
            },
          },
        ]}
      />
      {!valid && (
        <p role='alert'>
          {t('Enter a valid ratio with up to six decimal places.')}
        </p>
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
        disabled={!dirty || !valid || mutation.isPending || preview.isPending}
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
