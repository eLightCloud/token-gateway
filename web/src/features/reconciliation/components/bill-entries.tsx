import {
  ArrowLeft01Icon,
  ArrowRight01Icon,
  File02Icon,
} from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from '@/components/ui/empty'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import dayjs from '@/lib/dayjs'
import { formatBillingAmountFromQuota } from '@/lib/format'

import type { BillEntriesPage, BillEntry } from '../types'

const numberFormatter = new Intl.NumberFormat()

function billTypeLabel(type: BillEntry['type']): string {
  if (type === 'consume') return 'Consumption'
  return 'Refund'
}

function billTypeVariant(type: BillEntry['type']): 'secondary' | 'warning' {
  if (type === 'consume') return 'secondary'
  return 'warning'
}

function BillEntriesEmpty() {
  const { t } = useTranslation()
  return (
    <Empty className='min-h-72'>
      <EmptyHeader>
        <EmptyMedia variant='icon'>
          <HugeiconsIcon icon={File02Icon} strokeWidth={2} aria-hidden='true' />
        </EmptyMedia>
        <EmptyTitle>{t('No bill records in this period')}</EmptyTitle>
        <EmptyDescription>
          {t('Try another period or adjust the current filters.')}
        </EmptyDescription>
      </EmptyHeader>
    </Empty>
  )
}

export function BillEntriesTable(props: {
  page?: BillEntriesPage
  isLoading: boolean
  onPageChange: (page: number) => void
}) {
  const { t } = useTranslation()
  if (props.isLoading) {
    return (
      <div className='space-y-2 p-4'>
        <Skeleton className='h-10 w-full' />
        <Skeleton className='h-11 w-full' />
        <Skeleton className='h-11 w-full' />
        <Skeleton className='h-11 w-full' />
      </div>
    )
  }
  if (!props.page?.items.length) return <BillEntriesEmpty />

  const currentPage = props.page
  const pageCount = Math.max(
    1,
    Math.ceil(currentPage.total / currentPage.page_size)
  )

  return (
    <div className='min-h-0'>
      <div className='border-border border-b px-4 py-3'>
        <h3 className='font-medium'>{t('Underlying bill records')}</h3>
        <p className='text-muted-foreground mt-0.5 text-xs'>
          {t('Every record keeps the selected reconciliation subject.')}
        </p>
      </div>
      <Table className='min-w-[1320px]'>
        <TableHeader>
          <TableRow>
            <TableHead>{t('Time')}</TableHead>
            <TableHead>{t('Bill type')}</TableHead>
            <TableHead>Request ID</TableHead>
            <TableHead>{t('Organization')}</TableHead>
            <TableHead>{t('User')}</TableHead>
            <TableHead>{t('Model')}</TableHead>
            <TableHead>{t('Channel')}</TableHead>
            <TableHead>{t('API address')}</TableHead>
            <TableHead className='text-right'>{t('Input tokens')}</TableHead>
            <TableHead className='text-right'>{t('Output tokens')}</TableHead>
            <TableHead className='text-right'>{t('Quota')}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody className='[&>tr]:h-11'>
          {currentPage.items.map((entry) => (
            <TableRow key={`${entry.id}:${entry.request_id}`}>
              <TableCell className='text-muted-foreground'>
                {dayjs.unix(entry.created_at).format('YYYY-MM-DD HH:mm:ss')}
              </TableCell>
              <TableCell>
                <Badge variant={billTypeVariant(entry.type)}>
                  {t(billTypeLabel(entry.type))}
                </Badge>
              </TableCell>
              <TableCell className='max-w-56 font-mono text-xs'>
                <span className='block truncate'>
                  <span className='text-muted-foreground'>{t('Local')}:</span>{' '}
                  {entry.request_id || '—'}
                </span>
                {entry.upstream_request_id ? (
                  <span className='block truncate'>
                    <span className='text-muted-foreground'>
                      {t('Upstream')}:
                    </span>{' '}
                    {entry.upstream_request_id}
                  </span>
                ) : null}
              </TableCell>
              <TableCell>{entry.organization_name || '—'}</TableCell>
              <TableCell>
                <span>
                  {entry.username || '—'}{' '}
                  {entry.user_id ? (
                    <span className='text-muted-foreground text-xs'>
                      #{entry.user_id}
                    </span>
                  ) : null}
                </span>
                <span className='text-muted-foreground block text-xs'>
                  {entry.token_name || t('Token')} · #{entry.token_id || 0}
                </span>
              </TableCell>
              <TableCell>{entry.model_name || '—'}</TableCell>
              <TableCell>
                <span>
                  {entry.channel_name ||
                    (entry.channel_id ? `#${entry.channel_id}` : '—')}
                </span>
                {entry.channel_id && entry.channel_name ? (
                  <span className='text-muted-foreground block text-xs'>
                    #{entry.channel_id}
                  </span>
                ) : null}
              </TableCell>
              <TableCell className='max-w-64 font-mono text-xs'>
                <span className='block truncate'>
                  {entry.channel_api_address || t('Unknown API address')}
                </span>
              </TableCell>
              <TableCell className='text-right'>
                {numberFormatter.format(entry.prompt_tokens)}
              </TableCell>
              <TableCell className='text-right'>
                {numberFormatter.format(entry.completion_tokens)}
              </TableCell>
              <TableCell className='text-right font-medium'>
                {formatBillingAmountFromQuota(entry.quota)}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
      <div className='border-border flex flex-wrap items-center justify-between gap-3 border-t px-4 py-3'>
        <p className='text-muted-foreground text-sm'>
          {t('{{count}} bill records', {
            count: numberFormatter.format(currentPage.total),
          })}
        </p>
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
          <span className='min-w-24 text-center text-sm tabular-nums'>
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
      </div>
    </div>
  )
}
