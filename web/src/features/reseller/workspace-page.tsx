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
import { useMutation, useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { StaticDataTable } from '@/components/data-table'
import { SectionPageLayout } from '@/components/layout'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { RealtimePricingEditor } from '@/features/realtime-pricing/editor'
import { toIntlLocale } from '@/i18n/languages'
import { formatQuotaWithCurrency } from '@/lib/currency'
import { formatNumber } from '@/lib/format'
import { useAuthStore } from '@/stores/auth-store'

import {
  resellerKey,
  resellerPath,
  resellerRequest,
  type AdjustmentPage,
  type Workspace,
} from './api'
import { ResellerBillingTable } from './billing-page'
import { ResellerConfigEditor } from './config-editor'
import { ResellerQuotaDialog } from './quota-dialog'
import {
  ProcurementSummary,
  ResellerQueryStatus,
  ResellerTimestamp,
} from './shared'

export function ResellerAdjustments({
  userID,
  admin,
}: {
  userID: number
  admin: boolean
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const viewer = useAuthStore((s) => s.auth.user?.id)
  const [after, setAfter] = useState(0)
  const query = useQuery({
    meta: { errorToast: false },
    queryKey: resellerKey(viewer, 'adjustments', admin, userID, after),
    queryFn: () =>
      resellerRequest<AdjustmentPage>(
        `${resellerPath(admin)}/${userID}/quota-adjustments?after=${after}`
      ),
  })
  return (
    <div className='space-y-3'>
      <ResellerQueryStatus
        pending={query.isPending}
        error={query.isError}
        retry={() => void query.refetch()}
      />
      {query.data && (
        <StaticDataTable
          data={query.data.items}
          getRowKey={(row) => row.id}
          emptyContent={t('No results')}
          columns={[
            {
              id: 'time',
              header: t('Business time'),
              cell: (row) => <ResellerTimestamp value={row.occurred_at} />,
            },
            {
              id: 'mode',
              header: t('Operation'),
              cell: (row) => (row.mode === 'add' ? t('Add') : t('Subtract')),
            },
            {
              id: 'quota',
              header: t('Amount'),
              cell: (row) => formatQuotaWithCurrency(row.quota, { locale }),
            },
            {
              id: 'before',
              header: t('Balance before'),
              cell: (row) => formatQuotaWithCurrency(row.before, { locale }),
            },
            {
              id: 'after',
              header: t('Balance after'),
              cell: (row) => formatQuotaWithCurrency(row.after, { locale }),
            },
            {
              id: 'reason',
              header: t('Reason'),
              cell: (row) => (
                <span className='whitespace-normal'>{row.reason}</span>
              ),
            },
          ]}
        />
      )}
      <Button
        variant='outline'
        disabled={after === 0}
        onClick={() => setAfter(0)}
      >
        {t('First page')}
      </Button>
      <Button
        variant='outline'
        disabled={!query.data?.next_cursor}
        onClick={() => setAfter(query.data?.next_cursor ?? 0)}
      >
        {t('Next page')}
      </Button>
    </div>
  )
}

export function ResellerWorkspacePage({
  userID,
  admin = false,
}: {
  userID: number
  admin?: boolean
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const viewer = useAuthStore((s) => s.auth.user?.id)
  const [quotaOpen, setQuotaOpen] = useState(false)
  const [quotaSession, setQuotaSession] = useState(0)
  const [enabledOpen, setEnabledOpen] = useState(false)
  const [email, setEmail] = useState('')
  const query = useQuery({
    meta: { errorToast: false },
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
    queryKey: resellerKey(viewer, 'workspace', admin, userID),
    queryFn: () =>
      resellerRequest<Workspace>(`${resellerPath(admin)}/${userID}`),
  })
  const user = query.data?.user
  const enabled = user?.account ? user.account.enabled : !user?.agent_disabled
  let restrictionDescription: string
  if (!admin) {
    restrictionDescription = t(
      'This changes only the agent restriction. Platform restrictions remain in effect.'
    )
  } else if (enabled) {
    restrictionDescription = t(
      'Disabling this reseller stops new requests for all its customers. Existing billing continues.'
    )
  } else {
    restrictionDescription = t(
      'Restoring this reseller keeps each customer’s independent restrictions in effect.'
    )
  }
  const toggle = useMutation({
    meta: { errorToast: false },
    mutationFn: () =>
      resellerRequest(`${resellerPath(admin)}/${userID}/enabled`, 'put', {
        enabled: !enabled,
      }),
    onSuccess: () => {
      setEnabledOpen(false)
      void query.refetch()
    },
  })
  const activation = useMutation({
    meta: { errorToast: false },
    mutationFn: () =>
      resellerRequest(
        `${resellerPath(admin)}/${userID}/activation`,
        'post',
        email ? { email } : {}
      ),
    onSuccess: () => void query.refetch(),
  })
  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>
        {user?.username ?? t('Account details')}
      </SectionPageLayout.Title>
      <SectionPageLayout.Actions>
        <Link to={admin ? '/reseller/agents' : '/reseller/customers'}>
          {t('Back to list')}
        </Link>
        <Button
          variant='outline'
          onClick={() => {
            setQuotaSession((value) => value + 1)
            setQuotaOpen(true)
          }}
          disabled={!user}
        >
          {t('Adjust Quota')}
        </Button>
        <Button
          variant='outline'
          onClick={() => setEnabledOpen(true)}
          disabled={!user}
        >
          {enabled ? t('Disable') : t('Enable')}
        </Button>
      </SectionPageLayout.Actions>
      <SectionPageLayout.Content>
        <ResellerQueryStatus
          pending={query.isPending}
          error={query.isError}
          retry={() => void query.refetch()}
        />
        {query.data && user && (
          <div className='space-y-4'>
            {user.account && <ProcurementSummary account={user.account} />}
            <Tabs defaultValue='information'>
              <TabsList className='max-w-full overflow-x-auto'>
                <TabsTrigger value='information'>
                  {t('Basic information')}
                </TabsTrigger>
                <TabsTrigger value='configuration'>
                  {t('Model authorization and ratios')}
                </TabsTrigger>
                {!admin && (
                  <TabsTrigger value='billing'>
                    {t('Consumption details')}
                  </TabsTrigger>
                )}
                <TabsTrigger value='quota'>
                  {t('Quota adjustments')}
                </TabsTrigger>
              </TabsList>
              <TabsContent value='information'>
                <div className='space-y-3 py-3'>
                  <p>
                    {user.display_name} · {user.email}
                  </p>
                  <p>
                    {user.activation === 'active'
                      ? t('Active')
                      : t('Pending activation')}
                  </p>
                  <p>
                    {t('Available quota')}:{' '}
                    {formatQuotaWithCurrency(
                      user.account
                        ? user.account.balance - user.account.reserved
                        : user.quota,
                      { locale }
                    )}
                  </p>
                  {user.platform_disabled && <p>{t('Platform disabled')}</p>}
                  {user.agent_disabled && <p>{t('Agent disabled')}</p>}
                  {user.activation !== 'active' && (
                    <div className='max-w-md space-y-2'>
                      <Label htmlFor='activation-email'>
                        {t('Correct pending email')}
                      </Label>
                      <Input
                        id='activation-email'
                        type='email'
                        value={email}
                        onChange={(event) => setEmail(event.target.value)}
                      />
                      <Button
                        disabled={activation.isPending}
                        onClick={() => activation.mutate()}
                      >
                        {t('Resend activation email')}
                      </Button>
                    </div>
                  )}
                  {activation.isSuccess && (
                    <p role='status'>{t('Activation email sent')}</p>
                  )}
                  {activation.isError && (
                    <p role='alert'>{t('Failed to send activation email')}</p>
                  )}
                </div>
              </TabsContent>
              <TabsContent value='configuration' keepMounted>
                <ResellerConfigEditor
                  key={`${userID}:${query.data.config.revision}`}
                  userID={userID}
                  admin={admin}
                  consumptionEditable={
                    admin || query.data.pricing_phase !== 'active'
                  }
                  config={query.data.config}
                  candidates={query.data.candidates}
                  onSuccess={() => void query.refetch()}
                  onReload={async () => {
                    const result = await query.refetch()
                    if (result.isError) throw result.error
                  }}
                />
                {!admin && query.data.pricing_phase && (
                  <RealtimePricingEditor subject='user' subjectID={userID} />
                )}
                <section
                  aria-label={t('Configuration history')}
                  className='mt-4 space-y-2'
                >
                  <h3 className='font-medium'>{t('Configuration history')}</h3>
                  {query.data.history.length === 0 && <p>{t('No results')}</p>}
                  {query.data.history.map((entry) => (
                    <p
                      key={entry.revision}
                      className='text-muted-foreground text-sm'
                    >
                      {t('Revision')}: {formatNumber(entry.revision, locale)} ·{' '}
                      {t('Operator')}: {entry.operator_id} ·{' '}
                      <ResellerTimestamp value={entry.created_at} />
                    </p>
                  ))}
                </section>
              </TabsContent>
              {!admin && (
                <TabsContent value='billing'>
                  <ResellerBillingTable key={userID} customerID={userID} />
                </TabsContent>
              )}
              <TabsContent value='quota'>
                <ResellerAdjustments
                  key={userID}
                  userID={userID}
                  admin={admin}
                />
              </TabsContent>
            </Tabs>
            <ResellerQuotaDialog
              key={`${userID}:${quotaSession}`}
              userID={userID}
              admin={admin}
              quota={user.account?.balance ?? user.quota}
              open={quotaOpen}
              onOpenChange={setQuotaOpen}
              onSuccess={() => void query.refetch()}
            />
            <ConfirmDialog
              open={enabledOpen}
              onOpenChange={setEnabledOpen}
              title={enabled ? t('Disable account') : t('Enable account')}
              desc={restrictionDescription}
              destructive={enabled}
              isLoading={toggle.isPending}
              handleConfirm={() => toggle.mutate()}
            >
              {toggle.isError && <p role='alert'>{t('Operation failed')}</p>}
            </ConfirmDialog>
          </div>
        )}
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
