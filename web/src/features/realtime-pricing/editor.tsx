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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { StaticDataTable } from '@/components/data-table'
import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/ui/combobox'
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
import { PricingRuleDialog } from './rule-dialog'
import { PricingRulesTable } from './rules-table'
import { pricingSourceLabel, pricingMigrationReason } from './source'

export function RealtimePricingEditor(props: {
  subject: 'user' | 'organization'
  subjectID: number
  onDirtyChange?: (dirty: boolean) => void
}) {
  const { t } = useTranslation()
  const viewer = useAuthStore((s) => s.auth.user?.id)
  const query = useQuery({
    queryKey: ['realtime-pricing', viewer, props.subject, props.subjectID],
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
  reload: () => Promise<PricingWorkspace>
  onDirtyChange?: (dirty: boolean) => void
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const viewer = useAuthStore((s) => s.auth.user)
  const client = useQueryClient()
  const [baseline, setBaseline] = useState(props.data.config)
  const [items, setItems] = useState(props.data.config.items)
  const [filter, setFilter] = useState('')
  const [editing, setEditing] = useState<PricingRule | null>(null)
  const [creating, setCreating] = useState(false)
  const [deleting, setDeleting] = useState<string | null>(null)
  const [member, setMember] = useState('')
  const [history, setHistory] = useState(false)
  const [migrationConfirm, setMigrationConfirm] = useState<string | null>(null)
  const dirty = pricingRulesKey(items) !== pricingRulesKey(baseline.items)
  const [modalDirty, setModalDirty] = useState(false)
  const blocked = props.data.phase !== 'active'
  const save = useMutation({
    mutationFn: () =>
      savePricing(props.subject, props.subjectID, baseline.revision, items),
    meta: { errorToast: false },
    onSuccess: async (config) => {
      setBaseline(config)
      setItems(config.items)
      await client.invalidateQueries({ queryKey: ['realtime-preview'] })
      await client.invalidateQueries({ queryKey: ['reseller'] })
      await client.invalidateQueries({ queryKey: ['realtime-pricing'] })
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
  const draftKey = pricingRulesKey(items)
  const preview = useQuery({
    queryKey: [
      'realtime-preview',
      viewer?.id,
      props.subject,
      props.subjectID,
      props.data.config.revision,
      member,
      draftKey,
    ],
    queryFn: () =>
      previewPricing(
        props.subject,
        props.subjectID,
        items,
        Number(member) || 0
      ),
    enabled: !blocked,
    staleTime: 0,
    refetchOnWindowFocus: true,
    refetchOnReconnect: true,
    refetchInterval: 30_000,
    meta: { errorToast: false },
  })
  const memberChanged = getServerErrorSources(preview.error).some(
    (source) => source.code === 'REALTIME_PRICING_MEMBER_CHANGED'
  )
  const serverRevision = Math.max(
    props.data.config.revision,
    (props.subject === 'user'
      ? preview.data?.user_revision
      : preview.data?.organization_revision) ?? 0
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
    setEditing(null)
    setModalDirty(false)
    save.reset()
    await client.invalidateQueries({ queryKey: ['realtime-preview'] })
  }
  const editRule = useCallback((rule: PricingRule) => {
    setCreating(false)
    setEditing({ ...rule, channel_ids: [...rule.channel_ids] })
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
          'One ratio per model. Channels define discount coverage and do not restrict routing. User overrides organization; uncovered channels inherit.'
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
      <PricingRulesTable
        rules={filteredRules}
        candidates={props.data.candidates}
        disabled={blocked || save.isPending}
        onEdit={editRule}
        onDelete={setDeleting}
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
            : t(
                'Removing this rule restores system pricing. Personal overrides are preserved. Changes take effect after saving.'
              )
        }
        handleConfirm={() => {
          setItems((old) => old.filter((r) => r.model !== deleting))
          setDeleting(null)
        }}
      />
      <Button
        disabled={blocked || !dirty || save.isPending || editing !== null}
        onClick={() => save.mutate()}
      >
        {save.isPending ? t('Saving...') : t('Save changes')}
      </Button>
      {dirty && <p role='status'>{t('Unsaved changes')}</p>}
      {editing && (
        <PricingRuleDialog
          key={`${creating}:${editing.model}`}
          rule={editing}
          subject={props.subject}
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
      {!blocked && (
        <section className='space-y-2'>
          <h4 className='font-medium'>
            {props.subject === 'organization' && !member
              ? t('Organization preview without personal overrides')
              : t('Effective pricing preview')}
          </h4>
          {props.subject === 'organization' && (
            <Combobox
              options={(props.data.members ?? []).map((m) => ({
                value: String(m.id),
                label: `${m.name} #${m.id}`,
              }))}
              value={member}
              onValueChange={(value) => setMember(value ?? '')}
              placeholder={t('Select member for personal overrides')}
            />
          )}
          {member && (
            <Button variant='ghost' onClick={() => setMember('')}>
              {t('Organization preview without personal overrides')}
            </Button>
          )}
          <ResellerQueryStatus
            pending={preview.isPending}
            error={preview.isError && !memberChanged}
            retry={() => void preview.refetch()}
          />
          {memberChanged && (
            <div role='alert' className='space-y-2'>
              <p>
                {t(
                  'This user is no longer a current organization member. Select another member.'
                )}
              </p>
              <Button
                variant='outline'
                onClick={() => {
                  setMember('')
                  void props.reload().catch(() => undefined)
                }}
              >
                {t('Select another member')}
              </Button>
            </div>
          )}
          {preview.data && !preview.isError && (
            <>
              <div className='text-muted-foreground space-y-1 text-sm'>
                <p>
                  <ResellerTimestamp value={preview.data.generated_at} />
                </p>
                <p>
                  <span>
                    {t('User revision {{revision}}', {
                      revision: formatNumber(
                        preview.data.user_revision,
                        locale
                      ),
                    })}
                  </span>{' '}
                  ·{' '}
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
                <p>
                  {t(
                    'Preview explains the current configuration; future requests may use a different channel or version.'
                  )}
                </p>
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
