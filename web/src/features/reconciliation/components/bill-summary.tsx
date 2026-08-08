import { useTranslation } from 'react-i18next'

import { Skeleton } from '@/components/ui/skeleton'
import { formatBillingAmountFromQuota } from '@/lib/format'

import type { BillSummary } from '../types'

const numberFormatter = new Intl.NumberFormat()

function SummaryMetric(props: { label: string; value: string }) {
  return (
    <div className='min-w-0 px-4 py-4'>
      <p className='text-muted-foreground text-xs'>{props.label}</p>
      <p className='mt-1 truncate text-xl font-semibold tracking-tight tabular-nums'>
        {props.value}
      </p>
    </div>
  )
}

export function BillSummaryPanel(props: {
  summary?: BillSummary
  isLoading: boolean
}) {
  const { t } = useTranslation()
  if (props.isLoading) {
    return <Skeleton className='h-32 w-full rounded-none' />
  }
  if (!props.summary) return null

  const totalTokens =
    props.summary.prompt_tokens + props.summary.completion_tokens
  const isCustomer = props.summary.perspective === 'customer'
  let description = t('Recorded consumption grouped by current API address')
  if (isCustomer) {
    description = t('Customer charges and refunds from billing records')
  } else if (props.summary.perspective === 'upstream') {
    description = t('Recorded consumption routed through upstream channels')
  }

  return (
    <section className='border-border border-b'>
      <div className='flex flex-wrap items-center justify-between gap-2 border-b px-4 py-3'>
        <div>
          <h2 className='font-semibold'>{t('Overall usage')}</h2>
          <p className='text-muted-foreground mt-0.5 text-xs'>{description}</p>
        </div>
        {isCustomer ? (
          <p className='text-muted-foreground text-xs tabular-nums'>
            {t('Consumption')}:{' '}
            {formatBillingAmountFromQuota(props.summary.consume_quota)} ·{' '}
            {t('Refund')}:{' '}
            {formatBillingAmountFromQuota(props.summary.refund_quota)}
          </p>
        ) : null}
      </div>
      <div className='divide-border grid grid-cols-2 divide-x sm:grid-cols-3 xl:grid-cols-5'>
        <SummaryMetric
          label={isCustomer ? t('Net billed quota') : t('Routed billed quota')}
          value={formatBillingAmountFromQuota(props.summary.net_quota)}
        />
        <SummaryMetric
          label={isCustomer ? t('Bill records') : t('Consumption records')}
          value={numberFormatter.format(props.summary.record_count)}
        />
        <SummaryMetric
          label={t('Input tokens')}
          value={numberFormatter.format(props.summary.prompt_tokens)}
        />
        <SummaryMetric
          label={t('Output tokens')}
          value={numberFormatter.format(props.summary.completion_tokens)}
        />
        <SummaryMetric
          label={t('Total tokens')}
          value={numberFormatter.format(totalTokens)}
        />
      </div>
    </section>
  )
}
