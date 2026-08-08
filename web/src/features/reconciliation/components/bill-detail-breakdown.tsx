import { ArrowLeft01Icon, ArrowRight01Icon } from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { formatBillingAmountFromQuota } from '@/lib/format'

import type { BillDimension, BillGroupRow, BillGroupsPage } from '../types'

const numberFormatter = new Intl.NumberFormat()

function breakdownLabel(row: BillGroupRow, dimension: BillDimension): string {
  if (dimension === 'user_channel') {
    return row.channel_name || (row.channel_id ? `#${row.channel_id}` : '-')
  }
  return row.model_name || '-'
}

export function BillDetailBreakdown(props: {
  dimension: BillDimension
  page?: BillGroupsPage
  isLoading: boolean
  onPageChange: (page: number) => void
}) {
  const { t } = useTranslation()
  if (props.isLoading) {
    return <Skeleton className='m-4 h-40 w-auto' />
  }
  if (!props.page?.items.length) return null

  const currentPage = props.page
  const pageCount = Math.max(
    1,
    Math.ceil(currentPage.total / currentPage.page_size)
  )
  const heading = props.dimension === 'user_channel' ? t('Channel') : t('Model')

  return (
    <section className='border-border border-b'>
      <div className='flex items-center justify-between gap-3 px-4 py-3'>
        <div>
          <h3 className='font-medium'>{t('Further breakdown')}</h3>
          <p className='text-muted-foreground mt-0.5 text-xs'>
            {t('The reconciliation subject remains fixed in this breakdown.')}
          </p>
        </div>
        {pageCount > 1 ? (
          <div className='flex items-center gap-2'>
            <Button
              type='button'
              variant='outline'
              size='icon-sm'
              disabled={currentPage.page <= 1}
              aria-label={t('Previous page')}
              onClick={() => props.onPageChange(currentPage.page - 1)}
            >
              <HugeiconsIcon icon={ArrowLeft01Icon} strokeWidth={2} />
            </Button>
            <span className='text-muted-foreground text-xs tabular-nums'>
              {t('Page {{page}} of {{count}}', {
                page: currentPage.page,
                count: pageCount,
              })}
            </span>
            <Button
              type='button'
              variant='outline'
              size='icon-sm'
              disabled={currentPage.page >= pageCount}
              aria-label={t('Next page')}
              onClick={() => props.onPageChange(currentPage.page + 1)}
            >
              <HugeiconsIcon icon={ArrowRight01Icon} strokeWidth={2} />
            </Button>
          </div>
        ) : null}
      </div>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{heading}</TableHead>
            <TableHead className='text-right'>{t('Records')}</TableHead>
            <TableHead className='text-right'>{t('Total tokens')}</TableHead>
            <TableHead className='text-right'>{t('Billed quota')}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {currentPage.items.map((row) => (
            <TableRow key={`${props.dimension}:${row.key}`}>
              <TableCell className='font-mono text-xs'>
                {breakdownLabel(row, props.dimension)}
              </TableCell>
              <TableCell className='text-right tabular-nums'>
                {numberFormatter.format(row.record_count)}
              </TableCell>
              <TableCell className='text-right tabular-nums'>
                {numberFormatter.format(
                  row.prompt_tokens + row.completion_tokens
                )}
              </TableCell>
              <TableCell className='text-right font-medium tabular-nums'>
                {formatBillingAmountFromQuota(row.quota)}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </section>
  )
}
