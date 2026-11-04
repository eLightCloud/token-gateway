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
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { DatePicker } from '@/components/date-picker'
import { SectionPageLayout } from '@/components/layout'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import { beijingDateBoundaryToUnix } from '@/features/organizations/beijing-time'
import { toIntlLocale } from '@/i18n/languages'
import { api } from '@/lib/api'
import { formatQuotaWithCurrency } from '@/lib/currency'
import dayjs from '@/lib/dayjs'
import { useAuthStore } from '@/stores/auth-store'

import { resellerKey, resellerRequest, type BillingPage } from './api'
import { formatPersonalRatio } from './format'
import { useResellerFilter } from './search'
import {
  ProcurementSummary,
  ResellerQueryStatus,
  ResellerTimestamp,
} from './shared'
import { ResellerUsageFacts } from './usage-facts'

export function ResellerBillingTable({
  customerID,
  procurement = false,
}: {
  customerID?: number
  procurement?: boolean
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const userID = useAuthStore((s) => s.auth.user?.id)
  const endpoint = procurement
    ? '/api/reseller/self/procurement/events'
    : '/api/reseller/self/billing'
  const [requestID, setRequestID] = useResellerFilter<string>('request_id', '')
  const [model, setModel] = useResellerFilter<string>('model', '')
  const [kind, setKind] = useResellerFilter<string>('kind', '')
  const [posting, setPosting] = useResellerFilter<string>('posting', '')
  const [customer, setCustomer] = useResellerFilter<string>('customer', '')
  const [startDay, setStartDay] = useResellerFilter<string>('start_date', '')
  const [endDay, setEndDay] = useResellerFilter<string>('end_date', '')
  const [after, setAfter] = useResellerFilter<number>('after', 0)
  const [projectionRevision, setProjectionRevision] = useResellerFilter<
    number | undefined
  >('projection_revision', undefined)
  const [cutoff, setCutoff] = useResellerFilter(
    'cutoff',
    Math.floor(Date.now() / 1000) + 1
  )
  const params = new URLSearchParams({
    customer_id: String(customerID ?? (Number(customer) || 0)),
    posting,
    from: String(
      startDay ? (beijingDateBoundaryToUnix(startDay, false) ?? 0) : 0
    ),
    model,
    request_id: requestID,
    kind,
    after: String(after),
    until: String(
      endDay
        ? Math.min(
            cutoff,
            (beijingDateBoundaryToUnix(endDay, true) ?? cutoff) + 1
          )
        : cutoff
    ),
  })
  if (projectionRevision !== undefined) {
    params.set('projection_revision', String(projectionRevision))
  }
  const query = useQuery({
    meta: { errorToast: false },
    queryKey: resellerKey(userID, 'billing', procurement, params.toString()),
    refetchInterval:
      after === 0 && !endDay && projectionRevision === undefined ? 5000 : false,
    refetchIntervalInBackground: false,
    queryFn: () => {
      const liveParams = new URLSearchParams(params)
      if (after === 0 && !endDay && projectionRevision === undefined) {
        liveParams.set('until', String(Math.floor(Date.now() / 1000) + 1))
      }
      return resellerRequest<BillingPage>(`${endpoint}?${liveParams}`)
    },
  })
  const download = useMutation({
    meta: { errorToast: false },
    mutationFn: async () => {
      const exportParams = new URLSearchParams(params)
      exportParams.set('after', '0')
      if (query.data) {
        exportParams.set(
          'projection_revision',
          String(query.data.projection_revision)
        )
        exportParams.set('until', String(query.data.until))
      }
      const response = await api.get(`${endpoint}/export?${exportParams}`, {
        responseType: 'blob',
      })
      const url = URL.createObjectURL(response.data as Blob)
      const link = document.createElement('a')
      link.href = url
      link.download = 'reseller-billing.csv'
      link.click()
      URL.revokeObjectURL(url)
    },
  })
  return (
    <div className='space-y-4'>
      {!customerID && query.data && (
        <ProcurementSummary account={query.data.account} />
      )}
      <div className='flex flex-wrap items-end gap-3'>
        {!customerID && (
          <div className='space-y-2'>
            <Label htmlFor='billing-customer'>{t('Customer ID')}</Label>
            <Input
              id='billing-customer'
              type='number'
              min='1'
              value={customer}
              onChange={(event) => {
                setCustomer(event.target.value)
                setAfter(0)
                setProjectionRevision(undefined)
              }}
            />
          </div>
        )}
        <div className='space-y-2'>
          <Label>{t('Start date (Beijing)')}</Label>
          <DatePicker
            placeholder={t('Start date (Beijing)')}
            selected={startDay ? dayjs(startDay).toDate() : undefined}
            onSelect={(date) => {
              setStartDay(date ? dayjs(date).format('YYYY-MM-DD') : '')
              setAfter(0)
              setProjectionRevision(undefined)
            }}
          />
        </div>
        <div className='space-y-2'>
          <Label>{t('End date (Beijing)')}</Label>
          <DatePicker
            placeholder={t('End date (Beijing)')}
            selected={endDay ? dayjs(endDay).toDate() : undefined}
            onSelect={(date) => {
              setEndDay(date ? dayjs(date).format('YYYY-MM-DD') : '')
              setAfter(0)
              setProjectionRevision(undefined)
            }}
          />
        </div>
        <div className='space-y-2'>
          <Label htmlFor='billing-posting'>{t('Posting status')}</Label>
          <NativeSelect
            id='billing-posting'
            value={posting}
            onChange={(event) => {
              setPosting(event.target.value)
              setAfter(0)
              setProjectionRevision(undefined)
            }}
          >
            <option value=''>{t('All')}</option>
            <option value='pending'>{t('Pending posting')}</option>
            <option value='posted'>{t('Posted')}</option>
          </NativeSelect>
        </div>
        <div className='space-y-2'>
          <Label htmlFor='billing-request'>{t('Request ID')}</Label>
          <Input
            id='billing-request'
            value={requestID}
            maxLength={64}
            onChange={(event) => {
              setRequestID(event.target.value)
              setAfter(0)
              setProjectionRevision(undefined)
            }}
          />
        </div>
        <div className='space-y-2'>
          <Label htmlFor='billing-model'>{t('Model')}</Label>
          <Input
            id='billing-model'
            value={model}
            onChange={(event) => {
              setModel(event.target.value)
              setAfter(0)
              setProjectionRevision(undefined)
            }}
          />
        </div>
        <div className='space-y-2'>
          <Label htmlFor='billing-kind'>{t('Business status')}</Label>
          <NativeSelect
            id='billing-kind'
            className='border-input bg-background h-9 rounded-md border px-3 text-sm'
            value={kind}
            onChange={(event) => {
              setKind(event.target.value)
              setAfter(0)
              setProjectionRevision(undefined)
            }}
          >
            <option value=''>{t('All')}</option>
            <option value='reserve'>{t('Processing')}</option>
            <option value='settle'>{t('Settled')}</option>
            <option value='refund'>{t('Refund')}</option>
          </NativeSelect>
        </div>
        <Button
          variant='outline'
          onClick={() => {
            setAfter(0)
            setProjectionRevision(undefined)
            setCutoff(Math.floor(Date.now() / 1000) + 1)
          }}
        >
          {t('Refresh')}
        </Button>
        <Button
          variant='outline'
          disabled={download.isPending}
          onClick={() => download.mutate()}
        >
          {t('Export CSV')}
        </Button>
      </div>
      {download.isError && <p role='alert'>{t('Export failed')}</p>}
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
              id: 'customer',
              header: t('Customer'),
              cell: (row) => String(row.customer_id),
            },
            {
              id: 'model',
              header: t('Model'),
              cell: (row) => (
                <span className='block max-w-40 break-all whitespace-normal'>
                  {row.model}
                </span>
              ),
            },
            {
              id: 'channel',
              header: t('Channel'),
              cell: (row) => String(row.channel_id),
            },
            {
              id: 'ratios',
              header: t('Customer / procurement ratio'),
              cell: (row) =>
                `${formatPersonalRatio(row.customer_ratio, locale)} / ${formatPersonalRatio(row.agent_ratio, locale)}`,
            },
            {
              id: 'usage',
              header: t('Usage'),
              cell: (row) => <ResellerUsageFacts usage={row.usage} />,
            },
            {
              id: 'business',
              header: t('Business status'),
              cell: (row) =>
                ({
                  reserve: t('Processing'),
                  settle: t('Settled'),
                  refund: t('Refund'),
                })[row.kind],
            },
            {
              id: 'posting',
              header: t('Posting status'),
              cell: (row) =>
                row.processed_at ? t('Posted') : t('Pending posting'),
            },
            {
              id: 'customer-charge',
              header: t('Customer charge'),
              cell: (row) =>
                row.kind === 'reserve'
                  ? t('Pending settlement')
                  : formatQuotaWithCurrency(row.customer_delta, { locale }),
            },
            {
              id: 'procurement',
              header: t('Procurement expense'),
              cell: (row) =>
                row.kind === 'reserve'
                  ? t('Pending settlement')
                  : formatQuotaWithCurrency(row.spend_delta, { locale }),
            },
            {
              id: 'margin',
              header: t('Billing difference'),
              cell: (row) =>
                row.kind === 'reserve'
                  ? '—'
                  : formatQuotaWithCurrency(row.margin ?? 0, { locale }),
            },
            ...(procurement
              ? [
                  {
                    id: 'reservation',
                    header: t('Reservation change'),
                    cell: (row: import('./api').BillingRow) =>
                      formatQuotaWithCurrency(row.reserve_delta, { locale }),
                  },
                ]
              : []),
            {
              id: 'request',
              header: t('Request ID'),
              cell: (row) => (
                <span className='block max-w-40 break-all whitespace-normal'>
                  {row.request_id}
                </span>
              ),
            },
            {
              id: 'original',
              header: t('Original operation'),
              cell: (row) => (
                <span className='block max-w-40 break-all whitespace-normal'>
                  {row.original_operation_id || '—'}
                </span>
              ),
            },
            {
              id: 'time',
              header: t('Business time'),
              cell: (row) => <ResellerTimestamp value={row.occurred_at} />,
            },
          ]}
        />
      )}
      <div className='flex gap-2'>
        <Button
          variant='outline'
          disabled={after === 0}
          onClick={() => {
            setAfter(0)
            setProjectionRevision(undefined)
          }}
        >
          {t('First page')}
        </Button>
        <Button
          variant='outline'
          disabled={!query.data?.next_cursor}
          onClick={() => {
            setProjectionRevision(query.data?.projection_revision)
            setCutoff(query.data?.until ?? cutoff)
            setAfter(query.data?.next_cursor ?? 0)
          }}
        >
          {t('Next page')}
        </Button>
      </div>
    </div>
  )
}

export function ResellerBillingPage() {
  const { t } = useTranslation()
  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>{t('Customer billing')}</SectionPageLayout.Title>
      <SectionPageLayout.Content>
        <ResellerBillingTable />
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
