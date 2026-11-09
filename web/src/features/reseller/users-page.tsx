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
import { toast } from 'sonner'

import { StaticDataTable } from '@/components/data-table'
import { SectionPageLayout } from '@/components/layout'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import { useDebounce } from '@/hooks/use-debounce'
import { toIntlLocale } from '@/i18n/languages'
import { hasPermission } from '@/lib/admin-permissions'
import { formatQuotaWithCurrency } from '@/lib/currency'
import { useAuthStore } from '@/stores/auth-store'

import { useResellerActivationMail } from './activation-mail'
import { ResellerAgentControls } from './agent-controls'
import {
  hasResellerAgentPassword,
  isResellerAgentActivated,
} from './agent-status'
import {
  resellerKey,
  resellerPath,
  resellerRequest,
  type UserPage,
} from './api'
import { ResellerCreateDialog } from './create-account-dialog'
import { ResellerQuotaDialog } from './quota-dialog'
import { useResellerFilter } from './search'
import { ProcurementSummary, ResellerQueryStatus } from './shared'

export function ResellerUsersPage({ admin = false }: { admin?: boolean }) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const userID = useAuthStore((s) => s.auth.user?.id)
  const canAdjustQuota = useAuthStore(
    (s) => admin && hasPermission(s.auth.user, 'reseller', 'quota')
  )
  const [search, setSearch] = useResellerFilter<string>('search', '')
  const debouncedSearch = useDebounce(search, 300)
  const [activationFilter, setActivationFilter] = useResellerFilter<string>(
    'activation',
    ''
  )
  const [serviceFilter, setServiceFilter] = useResellerFilter<string>(
    'service_status',
    ''
  )
  const [after, setAfter] = useResellerFilter<number>('after', 0)
  const [create, setCreate] = useState(false)
  const [quotaTargetID, setQuotaTargetID] = useState<number>()
  const { sendActivationMail, sending } = useResellerActivationMail(
    admin,
    userID
  )
  const sendAccountEmailLabel = admin
    ? t('Reset Password')
    : t('Send activation email')
  const query = useQuery({
    meta: { errorToast: false },
    refetchInterval: 5000,
    refetchIntervalInBackground: false,
    queryKey: resellerKey(
      userID,
      'users',
      admin,
      debouncedSearch,
      activationFilter,
      serviceFilter,
      after
    ),
    queryFn: () =>
      resellerRequest<UserPage>(
        `${resellerPath(admin)}?${new URLSearchParams({ search: debouncedSearch, after: String(after), activation: activationFilter, service_status: serviceFilter })}`
      ),
  })
  const quotaTarget = canAdjustQuota
    ? query.data?.items.find((row) => row.user_id === quotaTargetID)
    : undefined

  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>
        {admin ? t('Reseller management') : t('Customer management')}
      </SectionPageLayout.Title>
      <SectionPageLayout.Actions>
        <Button onClick={() => setCreate(true)}>
          {admin ? t('Create reseller') : t('Create customer')}
        </Button>
      </SectionPageLayout.Actions>
      <SectionPageLayout.Content>
        <div className='space-y-4'>
          {query.data?.account && (
            <ProcurementSummary account={query.data.account} />
          )}
          <div className='flex flex-wrap items-end gap-x-3 gap-y-2'>
            <div className='max-w-xs min-w-48 flex-1 space-y-1'>
              <Label htmlFor='customer-search'>
                {t('Search account or email')}
              </Label>
              <Input
                id='customer-search'
                value={search}
                onChange={(event) => {
                  setSearch(event.target.value)
                  setAfter(0)
                }}
              />
            </div>
            <div className='space-y-1'>
              <Label htmlFor='customer-activation'>
                {admin ? t('Password') : t('Activation')}
              </Label>
              <NativeSelect
                id='customer-activation'
                value={activationFilter}
                onChange={(event) => {
                  setActivationFilter(event.target.value)
                  setAfter(0)
                }}
              >
                <option value=''>{t('All')}</option>
                <option value='pending'>
                  {admin ? t('Password not set') : t('Pending activation')}
                </option>
                {!admin && (
                  <option value='security_pending'>
                    {t('Pending MFA setup')}
                  </option>
                )}
                {admin ? (
                  <option value='password_set'>{t('Configured')}</option>
                ) : (
                  <option value='active'>{t('Active')}</option>
                )}
              </NativeSelect>
            </div>
            <div className='space-y-1'>
              <Label htmlFor='customer-service'>
                {admin ? t('Activation') : t('Service status')}
              </Label>
              <NativeSelect
                id='customer-service'
                value={serviceFilter}
                onChange={(event) => {
                  setServiceFilter(event.target.value)
                  setAfter(0)
                }}
              >
                <option value=''>{t('All')}</option>
                <option value='enabled'>
                  {admin ? t('Activated') : t('Enabled')}
                </option>
                {admin ? (
                  <option value='disabled'>{t('Disabled')}</option>
                ) : (
                  <>
                    <option value='platform_disabled'>
                      {t('Platform disabled')}
                    </option>
                    <option value='agent_disabled'>
                      {t('Agent disabled')}
                    </option>
                  </>
                )}
              </NativeSelect>
            </div>
          </div>
          <ResellerQueryStatus
            pending={query.isPending}
            error={query.isError}
            retry={() => void query.refetch()}
          />
          {query.data && (
            <StaticDataTable
              data={query.data.items}
              getRowKey={(row) => row.user_id}
              emptyContent={t('No results')}
              columns={[
                {
                  id: 'username',
                  header: t('Account'),
                  cell: (row) => (
                    <Link
                      className='text-primary underline-offset-4 hover:underline'
                      to={
                        admin
                          ? '/reseller/agents/$id'
                          : '/reseller/customers/$id'
                      }
                      params={{ id: String(row.user_id) }}
                    >
                      {row.username}
                    </Link>
                  ),
                },
                { id: 'email', header: t('Email'), cell: (row) => row.email },
                ...(admin
                  ? [
                      {
                        id: 'phone',
                        header: t('Mobile number'),
                        cell: (row: UserPage['items'][number]) =>
                          row.phone || '—',
                      },
                    ]
                  : []),
                {
                  id: 'activation',
                  header: admin ? t('Password') : t('Activation'),
                  cell: (row) => {
                    if (admin) {
                      return hasResellerAgentPassword(row.activation)
                        ? t('Configured')
                        : t('Password not set')
                    }
                    return (
                      (
                        {
                          active: t('Active'),
                          security_pending: t('Pending MFA setup'),
                        } as Record<string, string>
                      )[row.activation] ?? t('Pending activation')
                    )
                  },
                },
                {
                  id: 'service',
                  header: admin ? t('Activation') : t('Service status'),
                  cell: (row) => {
                    if (admin) {
                      return isResellerAgentActivated(row)
                        ? t('Activated')
                        : t('Disabled')
                    }
                    const agentDisabled = row.account
                      ? !row.account.enabled
                      : row.agent_disabled
                    return (
                      <>
                        {row.platform_disabled && (
                          <span>{t('Platform disabled')} </span>
                        )}
                        {agentDisabled && <span>{t('Agent disabled')} </span>}
                        {!row.platform_disabled &&
                          !agentDisabled &&
                          t('Enabled')}
                      </>
                    )
                  },
                },
                {
                  id: 'quota',
                  header: t('Available quota'),
                  cell: (row) =>
                    formatQuotaWithCurrency(
                      row.account
                        ? row.account.balance - row.account.reserved
                        : row.quota,
                      { locale }
                    ),
                },
                {
                  id: 'actions',
                  header: t('Actions'),
                  className: 'w-px text-end whitespace-nowrap',
                  cell: (row) => (
                    <div className='flex justify-end gap-2'>
                      {canAdjustQuota && row.account && (
                        <Button
                          variant='outline'
                          size='sm'
                          onClick={() => setQuotaTargetID(row.user_id)}
                        >
                          {t('Adjust Quota')}
                        </Button>
                      )}
                      {admin && (
                        <ResellerAgentControls
                          user={row}
                          onChanged={() => void query.refetch()}
                        />
                      )}
                      {row.activation === 'pending' && (
                        <Button
                          variant='outline'
                          size='sm'
                          disabled={
                            row.platform_disabled ||
                            sending.includes(row.user_id)
                          }
                          onClick={() => sendActivationMail(row.user_id)}
                        >
                          {sending.includes(row.user_id)
                            ? t('Sending...')
                            : sendAccountEmailLabel}
                        </Button>
                      )}
                    </div>
                  ),
                },
              ]}
            />
          )}
          <div className='flex gap-2'>
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
        </div>
        {quotaTarget?.account && (
          <ResellerQuotaDialog
            key={quotaTarget.user_id}
            userID={quotaTarget.user_id}
            username={quotaTarget.username}
            admin
            quota={quotaTarget.account.balance - quotaTarget.account.reserved}
            open
            onOpenChange={(open) => {
              if (!open) setQuotaTargetID(undefined)
            }}
            onSuccess={() => void query.refetch()}
          />
        )}
        <ResellerCreateDialog
          admin={admin}
          open={create}
          onOpenChange={setCreate}
          onCreated={(createdUserID) => {
            toast.success(t('User created successfully'))
            sendActivationMail(createdUserID)
            void query.refetch()
          }}
        />
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
