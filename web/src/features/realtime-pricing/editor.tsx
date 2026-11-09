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
import {
  useMutation,
  useQuery,
  useQueryClient,
  type QueryKey,
} from '@tanstack/react-query'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { StaticDataTable } from '@/components/data-table'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { ResellerConfigError } from '@/features/reseller/config-error'
import {
  ResellerQueryStatus,
  ResellerTimestamp,
} from '@/features/reseller/shared'
import { FormNavigationGuard } from '@/features/system-settings/components/form-navigation-guard'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'
import { ROLE } from '@/lib/roles'
import {
  getServerErrorSources,
  getServerErrorStatus,
} from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import {
  getPricing,
  pricingRulesKey,
  migratePricing,
  previewPricing,
  savePricing,
  type PricingRule,
  type PricingWorkspace,
} from './api'
import { PricingHistory } from './history'
import { PersonalPricingModelPicker } from './personal-rule-editor'
import { PricingRuleDialog } from './rule-dialog'
import { PricingRulesTable } from './rules-table'
import { pricingSourceLabel, pricingMigrationReason } from './source'
import { usePersonalRuleEditor } from './use-personal-rule-editor'

export function RealtimePricingEditor(props: {
  subject: 'user' | 'organization'
  subjectID: number
  onDirtyChange?: (dirty: boolean) => void
}) {
  const { t } = useTranslation()
  const viewer = useAuthStore((s) => s.auth.user?.id)
  const queryKey = ['realtime-pricing', viewer, props.subject, props.subjectID]
  const query = useQuery({
    queryKey,
    queryFn: () => getPricing(props.subject, props.subjectID),
    refetchOnWindowFocus: true,
    refetchOnReconnect: true,
    meta: { errorToast: false },
  })
  const denied =
    getServerErrorStatus(query.error) === 403 ||
    getServerErrorSources(query.error).some(
      (source) => source.code === 'RESELLER_FORBIDDEN'
    )
  return (
    <section className='space-y-4'>
      <ResellerQueryStatus
        pending={query.isPending}
        error={query.isError && !denied}
        retry={() => void query.refetch()}
      />
      {denied && (
        <p role='alert'>
          {t('You do not have permission to manage realtime pricing.')}
        </p>
      )}
      {query.data && !denied && (
        <PricingDraft
          key={`${viewer}:${props.subject}:${props.subjectID}:${query.data.phase}`}
          {...props}
          data={query.data}
          queryKey={queryKey}
          reload={async () => {
            const result = await query.refetch()
            if (result.isError || !result.data) throw result.error
            return result.data
          }}
        />
      )}
    </section>
  )
}

function PricingDraft(props: {
  subject: 'user' | 'organization'
  subjectID: number
  data: PricingWorkspace
  queryKey: QueryKey
  reload: () => Promise<PricingWorkspace>
  onDirtyChange?: (dirty: boolean) => void
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const viewer = useAuthStore((s) => s.auth.user)
  const client = useQueryClient()
  const [baseline, setBaseline] = useState(props.data.config)
  const [items, setItems] = useState(props.data.config.items)
  const personal = props.subject === 'user'
  const personalRule = usePersonalRuleEditor(items)
  const draft = personal ? personalRule.items : items
  const [filter, setFilter] = useState('')
  const [editing, setEditing] = useState<PricingRule | null>(null)
  const [creating, setCreating] = useState(false)
  const [deleting, setDeleting] = useState<string | null>(null)
  const [history, setHistory] = useState(false)
  const [migrationConfirm, setMigrationConfirm] = useState<string | null>(null)
  const draftKey = pricingRulesKey(draft)
  const dirty = draftKey !== pricingRulesKey(baseline.items)
  const [modalDirty, setModalDirty] = useState(false)
  const blocked = props.data.phase !== 'active'
  const save = useMutation({
    mutationFn: (rules: PricingRule[]) =>
      savePricing(props.subject, props.subjectID, baseline.revision, rules),
    meta: { errorToast: false },
    onSuccess: (config) => {
      setBaseline(config)
      setItems(config.items)
      if (personal) {
        personalRule.cancel()
        client.setQueryData<PricingWorkspace>(props.queryKey, (current) =>
          current && current.config.revision <= config.revision
            ? { ...current, config }
            : current
        )
      }
      const refresh = Promise.all([
        client.invalidateQueries({ queryKey: ['realtime-preview'] }),
        client.invalidateQueries({ queryKey: ['reseller'] }),
        client.invalidateQueries({ queryKey: ['realtime-pricing'] }),
      ])
      if (!personal) return refresh
    },
  })
  const migration = useMutation({
    mutationFn: (action: string) => migratePricing(action),
    meta: { errorToast: false },
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey: ['realtime-pricing'] })
      await client.invalidateQueries({ queryKey: ['realtime-preview'] })
      await client.invalidateQueries({ queryKey: ['reseller'] })
    },
  })
  const migrationReload = useMutation({
    mutationFn: props.reload,
    meta: { errorToast: false },
    onSuccess: () => migration.reset(),
  })
  const preview = useQuery({
    queryKey: [
      'realtime-preview',
      viewer?.id,
      props.subject,
      props.subjectID,
      props.data.config.revision,
      personal ? null : draftKey,
    ],
    queryFn: () => previewPricing(props.subject, props.subjectID, items),
    enabled: !blocked && !personal,
    staleTime: 0,
    refetchOnWindowFocus: true,
    refetchOnReconnect: true,
    refetchInterval: 30_000,
    meta: { errorToast: false },
  })
  const serverRevision = Math.max(
    props.data.config.revision,
    (personal ? undefined : preview.data?.organization_revision) ?? 0
  )
  const revisionError =
    serverRevision !== baseline.revision
      ? {
          code: 'RESELLER_REVISION_CONFLICT',
          data: { revision: serverRevision },
        }
      : undefined
  const reload = async () => {
    const data = await props.reload()
    setBaseline(data.config)
    setItems(data.config.items)
    personalRule.cancel()
    setEditing(null)
    setModalDirty(false)
    save.reset()
    await client.invalidateQueries({ queryKey: ['realtime-preview'] })
  }
  const editRule = useCallback((rule: PricingRule) => {
    setCreating(false)
    setEditing({ ...rule, channel_ids: [...rule.channel_ids] })
  }, [])
  const toggleRule = useCallback((model: string, enabled: boolean) => {
    setItems((old) =>
      old.map((rule) =>
        rule.model === model ? { ...rule, disabled: !enabled } : rule
      )
    )
  }, [])
  const filteredRules = useMemo(
    () => items.filter((r) => r.model.includes(filter)),
    [items, filter]
  )
  const onDirtyChange = props.onDirtyChange
  useEffect(() => {
    onDirtyChange?.(dirty || modalDirty)
  }, [dirty, modalDirty, onDirtyChange])
  const models = useMemo(
    () =>
      [
        ...new Set(
          [
            ...props.data.candidates.flatMap((c) => c.models.split(',')),
            ...items.map((r) => r.model),
          ].filter(Boolean)
        ),
      ].sort(),
    [props.data.candidates, items]
  )
  return (
    <div className='space-y-4'>
      <FormNavigationGuard when={dirty || modalDirty} />
      <h3 className='font-medium'>{t('Realtime model pricing')}</h3>
      <p className='text-muted-foreground text-sm'>
        {t(
          'One ratio per model. Selected channels define the discount scope. Routing is unchanged.'
        )}
        {props.subject === 'user' && (
          <>
            {' '}
            {t(
              'Enabled personal rules take priority on selected channels. Disabled rules keep their settings and inherit pricing.'
            )}
          </>
        )}
      </p>
      <p className='text-muted-foreground text-sm'>
        {t(
          'Ratios must be greater than 0 and at most 5, with up to two decimal places. Trusted requests retain existing pre-consumption bypass.'
        )}
      </p>
      {blocked && (
        <div role='status' className='space-y-2 rounded border p-3'>
          <p>
            {t(
              'Realtime pricing is not active. Existing pricing remains in effect.'
            )}
          </p>
          {viewer?.role === ROLE.SUPER_ADMIN && (
            <div className='flex flex-wrap gap-2'>
              <Button
                disabled={migration.isPending}
                onClick={() =>
                  setMigrationConfirm(
                    props.data.phase === 'legacy' ? 'freeze' : 'activate'
                  )
                }
              >
                {props.data.phase === 'legacy'
                  ? t('Freeze legacy consumption pricing')
                  : t('Validate migration and activate')}
              </Button>
              {props.data.phase === 'frozen' && (
                <Button
                  variant='outline'
                  disabled={migration.isPending}
                  onClick={() => setMigrationConfirm('resume')}
                >
                  {t('Resume legacy configuration')}
                </Button>
              )}
            </div>
          )}
        </div>
      )}
      <ConfirmDialog
        open={migrationConfirm !== null}
        onOpenChange={(open) => {
          if (!open) setMigrationConfirm(null)
        }}
        title={t('Confirm pricing migration')}
        desc={t(
          'Ensure all instances support this release and configuration is backed up. Activation migrates personal consumption rules; conflicts prevent activation. Existing organization channel factors remain unchanged.'
        )}
        handleConfirm={() => {
          if (migrationConfirm) migration.mutate(migrationConfirm)
          setMigrationConfirm(null)
        }}
      />
      {migration.data?.conflicts.map((c) => (
        <p
          role='alert'
          key={`${c.user_id}:${c.model}:${c.channel_id}:${c.reason}`}
        >
          #{c.user_id} · {c.model} · #{c.channel_id} · {c.ratio} ·{' '}
          {pricingMigrationReason(c.reason, t)}
        </p>
      ))}
      {migration.error && (
        <div role='alert' className='space-y-2'>
          <p>
            {t(
              'Migration could not be confirmed. Reload to check the current phase before retrying.'
            )}
          </p>
          <Button
            variant='outline'
            disabled={migrationReload.isPending}
            onClick={() => migrationReload.mutate()}
          >
            {t('Reload configuration')}
          </Button>
        </div>
      )}
      <ResellerConfigError
        error={save.error ?? revisionError}
        onReload={reload}
      />
      {personal ? (
        <PersonalPricingModelPicker
          models={models}
          rules={items}
          disabled={blocked || save.isPending || !!personalRule.edit}
          onSelect={personalRule.begin}
        />
      ) : (
        <div className='flex flex-wrap gap-2'>
          <Input
            aria-label={t('Search models')}
            placeholder={t('Search models')}
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            className='max-w-sm'
          />
          <Button
            disabled={blocked || save.isPending}
            onClick={() => {
              setCreating(true)
              setEditing({ model: '', ratio: '1', channel_ids: [] })
            }}
          >
            {t('Add model')}
          </Button>
        </div>
      )}
      <PricingRulesTable
        rules={personal ? draft : filteredRules}
        candidates={props.data.candidates}
        disabled={blocked || save.isPending}
        emptyDescription={
          personal ? t('Type a model name to add it') : undefined
        }
        inlineEdit={personal ? personalRule.edit : undefined}
        onEdit={personal ? personalRule.begin : editRule}
        onDelete={setDeleting}
        onToggle={props.subject === 'user' ? toggleRule : undefined}
      />
      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(open) => {
          if (!open) setDeleting(null)
        }}
        title={t('Restore inherited pricing')}
        desc={
          props.subject === 'user'
            ? t(
                'Removing this rule restores organization or system pricing on its channels. Changes take effect after saving.'
              )
            : t('The organization default discount is removed after saving.')
        }
        handleConfirm={() => {
          setItems((old) => old.filter((r) => r.model !== deleting))
          setDeleting(null)
        }}
      />
      <Button
        disabled={
          blocked ||
          !dirty ||
          save.isPending ||
          editing !== null ||
          (personal && !personalRule.isValid)
        }
        onClick={() => save.mutate(draft)}
      >
        {save.isPending ? t('Saving...') : t('Save changes')}
      </Button>
      {dirty && <p role='status'>{t('Unsaved changes')}</p>}
      {personal && !dirty && save.isSuccess && (
        <p role='status'>{t('Saved successfully')}</p>
      )}
      {editing && (
        <PricingRuleDialog
          key={`${creating}:${editing.model}`}
          rule={editing}
          configuredRules={items}
          onSelectConfigured={editRule}
          creating={creating}
          models={models}
          candidates={props.data.candidates}
          onDirtyChange={setModalDirty}
          onClose={() => {
            setEditing(null)
            setModalDirty(false)
          }}
          onApply={(rule) => {
            setItems((old) =>
              old.some((r) => r.model === rule.model)
                ? old.map((r) => (r.model === rule.model ? rule : r))
                : [...old, rule]
            )
            setEditing(null)
            setModalDirty(false)
          }}
        />
      )}
      {!blocked && !personal && (
        <section className='space-y-2'>
          <h4 className='font-medium'>{t('Organization default discounts')}</h4>
          <ResellerQueryStatus
            pending={preview.isPending}
            error={preview.isError}
            retry={() => void preview.refetch()}
          />
          {preview.data && !preview.isError && (
            <>
              <div className='text-muted-foreground space-y-1 text-sm'>
                <p>
                  <ResellerTimestamp value={preview.data.generated_at} />
                </p>
                <p>
                  <span>
                    {t('Organization revision {{revision}}', {
                      revision: formatNumber(
                        preview.data.organization_revision,
                        locale
                      ),
                    })}
                  </span>
                </p>
                {preview.data.organization_id > 0 && (
                  <p>
                    {t('Organization #{{id}}', {
                      id: preview.data.organization_id,
                    })}
                  </p>
                )}
              </div>
              <StaticDataTable
                data={preview.data.rows}
                getRowKey={(r) => `${r.model}:${r.channel_id}`}
                emptyContent={t('No results')}
                columns={[
                  { id: 'model', header: t('Model'), cell: (r) => r.model },
                  {
                    id: 'channel',
                    header: t('Channel'),
                    cell: (r) =>
                      props.data.candidates.find((c) => c.id === r.channel_id)
                        ?.name || `#${r.channel_id}`,
                  },
                  {
                    id: 'ratio',
                    header: t('Ratio'),
                    cell: (r) => `${formatNumber(Number(r.ratio), locale)}×`,
                  },
                  {
                    id: 'source',
                    header: t('Pricing source'),
                    cell: (r) => {
                      return pricingSourceLabel(r.source, t)
                    },
                  },
                ]}
              />
            </>
          )}
        </section>
      )}
      <Button variant='outline' onClick={() => setHistory((old) => !old)}>
        {t('Configuration history')}
      </Button>
      {history && (
        <PricingHistory
          entries={props.data.history}
          candidates={props.data.candidates}
        />
      )}
    </div>
  )
}
