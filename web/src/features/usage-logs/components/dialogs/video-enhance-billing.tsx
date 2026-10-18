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
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { toIntlLocale } from '@/i18n/languages'
import { api } from '@/lib/api'
import { formatBillingCurrencyFromUSD } from '@/lib/currency'
import { formatLogQuota, formatNumber } from '@/lib/format'
import { requireServerSuccess } from '@/lib/server-error-message'

import { DetailRow, DetailSection } from './log-detail-layout'

type VideoBill = {
  task_id: string
  billing_status:
    | 'processing'
    | 'settling'
    | 'settled'
    | 'refunding'
    | 'refunded'
  final_quota: number | null
  seconds?: number
  resolution?: string
  fps?: number
  unit_price_usd?: number
  group_ratio?: number
  organization_discount?: number
  admin_info?: {
    stage: string
    attempts: number
    error_code?: string
    generation_usage?: Record<string, number>
  }
  root_info?: {
    attempts: {
      number: number
      status: string
      error_code: string
      provider_error_code?: string
      request_id?: string
    }[]
  }
}

const statusLabels = {
  processing: 'Processing',
  settling: 'Settlement pending',
  settled: 'Settled',
  refunding: 'Refund processing',
  refunded: 'Fully refunded',
} as const

export function VideoEnhanceBilling(props: {
  taskId: string
  open: boolean
  isAdmin: boolean
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const query = useQuery({
    queryKey: ['video-enhance-task-bill', props.taskId],
    enabled: props.open && Boolean(props.taskId),
    queryFn: async () => {
      const response = await api.get<{
        success: boolean
        message?: string
        data: VideoBill
      }>(`/api/log/task_billing/${encodeURIComponent(props.taskId)}`)
      return requireServerSuccess(response.data).data
    },
    refetchInterval: (query) => {
      const status = query.state.data?.billing_status
      return status === 'settled' || status === 'refunded' ? false : 5000
    },
  })
  if (query.isPending) {
    return <LoadingState message={t('Loading billing details...')} />
  }
  if (!query.data) {
    return (
      <ErrorState
        title={t('Failed to load billing details')}
        onRetry={() => void query.refetch()}
      />
    )
  }
  const bill = query.data
  return (
    <DetailSection label={t('Final video billing')}>
      <DetailRow
        label={t('Billing status')}
        value={t(statusLabels[bill.billing_status])}
      />
      {bill.seconds !== undefined && (
        <DetailRow
          label={t('Billable video duration')}
          value={`${new Intl.NumberFormat(locale, { maximumFractionDigits: 3 }).format(bill.seconds)} ${t('seconds')}`}
        />
      )}
      {bill.resolution && (
        <DetailRow label={t('Output resolution')} value={bill.resolution} />
      )}
      {bill.unit_price_usd !== undefined && (
        <DetailRow
          label={t('Video unit price')}
          value={`${formatBillingCurrencyFromUSD(bill.unit_price_usd, { digitsLarge: 6, digitsSmall: 8 })} / ${t('second')}`}
        />
      )}
      {bill.group_ratio !== undefined && (
        <DetailRow
          label={t('Group ratio')}
          value={formatNumber(bill.group_ratio, locale)}
        />
      )}
      {bill.organization_discount !== undefined && (
        <DetailRow
          label={t('Organization discount')}
          value={formatNumber(bill.organization_discount, locale)}
        />
      )}
      {bill.final_quota !== null && (
        <DetailRow
          label={t('Final net consumption')}
          value={formatLogQuota(bill.final_quota)}
        />
      )}
      <p className='text-muted-foreground text-xs'>
        {t(
          'Linked entries record reservations, adjustments or refunds for the same task. Each entry amount is not the final task total.'
        )}
      </p>
      {props.isAdmin && bill.admin_info && (
        <>
          <DetailRow
            label={t('Enhancement attempts')}
            value={formatNumber(bill.admin_info.attempts, locale)}
          />
          <DetailRow
            label={t('Processing stage')}
            value={bill.admin_info.stage}
          />
          {bill.admin_info.error_code && (
            <DetailRow
              label={t('Last processing error')}
              value={bill.admin_info.error_code}
              mono
            />
          )}
        </>
      )}
      {props.isAdmin &&
        bill.root_info?.attempts.map((attempt) => (
          <div key={attempt.number} className='space-y-1'>
            <DetailRow
              label={t('Enhancement attempt {{number}}', {
                number: formatNumber(attempt.number, locale),
              })}
              value={attempt.error_code || attempt.status}
              mono
            />
            {attempt.provider_error_code && (
              <DetailRow
                label={t('Upstream error code')}
                value={attempt.provider_error_code}
                mono
              />
            )}
            {attempt.request_id && (
              <DetailRow
                label={t('Request ID')}
                value={attempt.request_id}
                mono
              />
            )}
          </div>
        ))}
    </DetailSection>
  )
}
