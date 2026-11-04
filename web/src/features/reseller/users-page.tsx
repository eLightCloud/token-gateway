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

import { StaticDataTable } from '@/components/data-table'
import { Dialog } from '@/components/dialog'
import { SectionPageLayout } from '@/components/layout'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import { useDebounce } from '@/hooks/use-debounce'
import { toIntlLocale } from '@/i18n/languages'
import { formatQuotaWithCurrency } from '@/lib/currency'
import { useAuthStore } from '@/stores/auth-store'

import {
  resellerKey,
  resellerPath,
  resellerRequest,
  type UserPage,
} from './api'
import { useResellerFilter } from './search'
import { ProcurementSummary, ResellerQueryStatus } from './shared'

export function ResellerUsersPage({ admin = false }: { admin?: boolean }) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const userID = useAuthStore((s) => s.auth.user?.id)
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
  const [mailFailed, setMailFailed] = useState(false)
  const [username, setUsername] = useState('')
  const [email, setEmail] = useState('')
  const [displayName, setDisplayName] = useState('')
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
  const mutation = useMutation({
    meta: { errorToast: false },
    mutationFn: () =>
      resellerRequest<{ activation_mail_sent: boolean }>(
        resellerPath(admin),
        'post',
        {
          username,
          email,
          display_name: displayName,
        }
      ),
    onSuccess: (result) => {
      setMailFailed(!result.activation_mail_sent)
      setCreate(false)
      setUsername('')
      setEmail('')
      setDisplayName('')
      void query.refetch()
    },
  })
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
          {mailFailed && (
            <p role='alert'>
              {t(
                'Account created, but activation email could not be sent. Open the account to resend it.'
              )}
            </p>
          )}
          {query.data?.account && (
            <ProcurementSummary account={query.data.account} />
          )}
          <div className='max-w-md space-y-2'>
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
          <div className='flex flex-wrap gap-3'>
            <div className='space-y-2'>
              <Label htmlFor='customer-activation'>{t('Activation')}</Label>
              <NativeSelect
                id='customer-activation'
                value={activationFilter}
                onChange={(event) => {
                  setActivationFilter(event.target.value)
                  setAfter(0)
                }}
              >
                <option value=''>{t('All')}</option>
                <option value='pending'>{t('Pending activation')}</option>
                <option value='security_pending'>
                  {t('Pending MFA setup')}
                </option>
                <option value='active'>{t('Active')}</option>
              </NativeSelect>
            </div>
            <div className='space-y-2'>
              <Label htmlFor='customer-service'>{t('Service status')}</Label>
              <NativeSelect
                id='customer-service'
                value={serviceFilter}
                onChange={(event) => {
                  setServiceFilter(event.target.value)
                  setAfter(0)
                }}
              >
                <option value=''>{t('All')}</option>
                <option value='enabled'>{t('Enabled')}</option>
                <option value='platform_disabled'>
                  {t('Platform disabled')}
                </option>
                <option value='agent_disabled'>{t('Agent disabled')}</option>
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
                {
                  id: 'activation',
                  header: t('Activation'),
                  cell: (row) =>
                    (
                      ({
                        active: t('Active'),
                        security_pending: t('Pending MFA setup'),
                      }) as Record<string, string>
                    )[row.activation] ?? t('Pending activation'),
                },
                {
                  id: 'service',
                  header: t('Service status'),
                  cell: (row) => {
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
        <Dialog
          open={create}
          onOpenChange={setCreate}
          title={admin ? t('Create reseller') : t('Create customer')}
          description={t(
            'An activation email lets the account holder set a password and configure MFA.'
          )}
        >
          <form
            className='space-y-4'
            onSubmit={(event) => {
              event.preventDefault()
              mutation.mutate()
            }}
          >
            <div className='space-y-2'>
              <Label htmlFor='create-username'>{t('Username')}</Label>
              <Input
                id='create-username'
                required
                maxLength={20}
                value={username}
                onChange={(event) => setUsername(event.target.value)}
              />
            </div>
            <div className='space-y-2'>
              <Label htmlFor='create-name'>{t('Display Name')}</Label>
              <Input
                id='create-name'
                maxLength={20}
                value={displayName}
                onChange={(event) => setDisplayName(event.target.value)}
              />
            </div>
            <div className='space-y-2'>
              <Label htmlFor='create-email'>{t('Email')}</Label>
              <Input
                id='create-email'
                required
                type='email'
                maxLength={50}
                value={email}
                onChange={(event) => setEmail(event.target.value)}
              />
            </div>
            {mutation.isError && (
              <p role='alert'>{t('Failed to create account')}</p>
            )}
            <Button type='submit' disabled={mutation.isPending}>
              {t('Create')}
            </Button>
          </form>
        </Dialog>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
