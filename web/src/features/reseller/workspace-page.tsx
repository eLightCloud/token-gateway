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
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { ErrorState } from '@/components/error-state'
import { SectionPageLayout } from '@/components/layout'
import { Badge } from '@/components/ui/badge'
import { Button, buttonVariants } from '@/components/ui/button'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { RealtimePricingEditor } from '@/features/realtime-pricing/editor'
import { toIntlLocale } from '@/i18n/languages'
import { formatQuotaWithCurrency } from '@/lib/currency'
import { formatNumber } from '@/lib/format'
import { useAuthStore } from '@/stores/auth-store'

import { ResellerAgentConfigHistory } from './agent-config-history'
import { ResellerAgentControls } from './agent-controls'
import { ResellerAgentProcurementRatios } from './agent-discounts'
import { ResellerAgentInformation } from './agent-information'
import {
  hasResellerAgentPassword,
  resellerCustomerStatus,
} from './agent-status'
import {
  resellerKey,
  resellerPath,
  resellerRequest,
  type AdjustmentPage,
  type Workspace,
} from './api'
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
  const [tab, setTab] = useState('information')
  const query = useQuery({
    meta: { errorToast: false },
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
    queryKey: resellerKey(viewer, 'workspace', admin, userID),
    queryFn: () =>
      resellerRequest<Workspace>(`${resellerPath(admin)}/${userID}`),
  })
  const user = query.data?.user
  if (admin && user && !user.account) {
    return (
      <SectionPageLayout>
        <SectionPageLayout.Title>
          {t('Account details')}
        </SectionPageLayout.Title>
        <SectionPageLayout.Content>
          <ErrorState
            title={t('Reseller account not found')}
            action={
              <Link
                to='/reseller/agents'
                className={buttonVariants({ variant: 'outline' })}
              >
                {t('Back to list')}
              </Link>
            }
          />
        </SectionPageLayout.Content>
      </SectionPageLayout>
    )
  }
  const configEditor = query.data && (
    <ResellerConfigEditor
      key={`${userID}:${query.data.config.revision}`}
      userID={userID}
      admin={admin}
      procurement={admin}
      consumptionEditable={admin || query.data.pricing_phase !== 'active'}
      config={query.data.config}
      candidates={query.data.candidates}
      onSuccess={() => void query.refetch()}
      onReload={async () => {
        const result = await query.refetch()
        if (result.isError) throw result.error
      }}
    />
  )
  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>
        {user?.username ?? t('Account details')}
      </SectionPageLayout.Title>
      <SectionPageLayout.Actions>
        <Link to={admin ? '/reseller/agents' : '/reseller/customers'}>
          {t('Back to list')}
        </Link>
        {!admin && user && (
          <Link
            className={buttonVariants({ variant: 'outline' })}
            to='/reseller/billing'
            search={{ customer: String(userID) }}
          >
            {t('View customer bill')}
          </Link>
        )}
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
        {user && (
          <ResellerAgentControls
            user={user}
            customer={!admin}
            onChanged={() => void query.refetch()}
          />
        )}
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
            {!admin && (
              <div className='flex flex-wrap items-center gap-2 text-sm'>
                <Badge variant='outline'>
                  {hasResellerAgentPassword(user.activation)
                    ? `${t('Password')} · ${t('Configured')}`
                    : t('Password not set')}
                </Badge>
                <Badge variant='secondary'>
                  {t(resellerCustomerStatus(user))}
                </Badge>
                <span>
                  {t('Available quota')}{' '}
                  <strong>
                    {formatQuotaWithCurrency(user.quota, { locale })}
                  </strong>
                </span>
              </div>
            )}
            <Tabs value={tab} onValueChange={setTab}>
              <TabsList className='max-w-full overflow-x-auto'>
                <TabsTrigger value='information'>
                  {admin ? t('Basic information') : t('Customer overview')}
                </TabsTrigger>
                <TabsTrigger value='configuration'>
                  {admin
                    ? t('Model authorization and ratios')
                    : t('Model authorization and discounts')}
                </TabsTrigger>
                <TabsTrigger value='quota'>
                  {t('Quota adjustments')}
                </TabsTrigger>
              </TabsList>
              <TabsContent value='information'>
                <ResellerAgentInformation user={user} customer={!admin} />
              </TabsContent>
              <TabsContent value='configuration' keepMounted>
                {admin ? (
                  <ResellerAgentProcurementRatios
                    items={query.data.config.items}
                    candidates={query.data.candidates}
                  >
                    {configEditor}
                  </ResellerAgentProcurementRatios>
                ) : (
                  configEditor
                )}
                {!admin && query.data.pricing_phase && (
                  <RealtimePricingEditor subject='user' subjectID={userID} />
                )}
                {admin && tab === 'configuration' && (
                  <ResellerAgentConfigHistory
                    userID={userID}
                    revision={query.data.config.revision}
                  />
                )}
                {!admin && (
                  <section
                    aria-label={t('Configuration history')}
                    className='mt-4 space-y-2'
                  >
                    <h3 className='font-medium'>
                      {t('Configuration history')}
                    </h3>
                    {query.data.history.length === 0 && (
                      <p>{t('No results')}</p>
                    )}
                    {query.data.history.map((entry) => (
                      <p
                        key={entry.revision}
                        className='text-muted-foreground text-sm'
                      >
                        {t('Revision')}: {formatNumber(entry.revision, locale)}{' '}
                        · {t('Operator')}: {entry.operator_id} ·{' '}
                        <ResellerTimestamp value={entry.created_at} />
                      </p>
                    ))}
                  </section>
                )}
              </TabsContent>
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
          </div>
        )}
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
