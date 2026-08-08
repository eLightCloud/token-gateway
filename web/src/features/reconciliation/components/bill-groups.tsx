import {
  ArrowLeft01Icon,
  ArrowRight01Icon,
  ArrowRight02Icon,
} from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
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
import { formatBillingAmountFromQuota } from '@/lib/format'
import { cn } from '@/lib/utils'

import { BILL_DIMENSIONS } from '../constants'
import type {
  BillDimension,
  BillGroupRow,
  BillGroupsPage,
  BillPerspective,
} from '../types'

const numberFormatter = new Intl.NumberFormat()

function primaryHeading(dimension: BillDimension): string {
  if (dimension.startsWith('user')) return 'Customer'
  if (dimension.startsWith('upstream_channel')) return 'API address'
  return 'Channel'
}

function secondaryHeading(dimension: BillDimension): string | undefined {
  if (dimension === 'user_model' || dimension === 'channel_model') {
    return 'Model'
  }
  if (
    dimension === 'user_channel' ||
    dimension === 'upstream_channel' ||
    dimension === 'upstream_channel_model'
  ) {
    return 'Channel'
  }
  return undefined
}

function tertiaryHeading(dimension: BillDimension): string | undefined {
  if (dimension === 'upstream_channel_model') return 'Model'
  return undefined
}

function channelLabel(row: BillGroupRow): string {
  if (row.channel_name) return row.channel_name
  if (row.channel_id) return `#${row.channel_id}`
  return '-'
}

function secondaryLabel(row: BillGroupRow, dimension: BillDimension): string {
  if (dimension === 'user_model' || dimension === 'channel_model') {
    return row.model_name || '-'
  }
  return channelLabel(row)
}

export function BillGroupsTable(props: {
  perspective: BillPerspective
  dimension: BillDimension
  page?: BillGroupsPage
  isLoading: boolean
  onDimensionChange: (dimension: BillDimension) => void
  onPageChange: (page: number) => void
  onOpenDetails: (row: BillGroupRow) => void
}) {
  const { t } = useTranslation()
  const dimensions = BILL_DIMENSIONS[props.perspective]
  const currentPage = props.page
  const secondHeading = secondaryHeading(props.dimension)
  const thirdHeading = tertiaryHeading(props.dimension)
  const pageCount = Math.max(
    1,
    Math.ceil((currentPage?.total ?? 0) / (currentPage?.page_size ?? 20))
  )

  return (
    <section>
      <div className='flex flex-wrap items-center justify-between gap-3 border-b px-4 py-3'>
        <div>
          <h2 className='font-semibold'>{t('Usage breakdown')}</h2>
          <p className='text-muted-foreground mt-0.5 text-xs'>
            {t('Select a row to inspect the underlying bill records.')}
          </p>
        </div>
        {dimensions.length > 1 ? (
          <div className='border-border flex rounded-lg border p-0.5'>
            {dimensions.map((item) => (
              <Button
                key={item.value}
                type='button'
                size='sm'
                variant={props.dimension === item.value ? 'secondary' : 'ghost'}
                aria-pressed={props.dimension === item.value}
                onClick={() => props.onDimensionChange(item.value)}
              >
                {t(item.labelKey)}
              </Button>
            ))}
          </div>
        ) : null}
      </div>

      {props.isLoading && (
        <div className='space-y-2 p-4'>
          <Skeleton className='h-10 w-full' />
          <Skeleton className='h-11 w-full' />
          <Skeleton className='h-11 w-full' />
        </div>
      )}
      {!props.isLoading && !currentPage?.items.length && (
        <Empty className='min-h-72'>
          <EmptyHeader>
            <EmptyTitle>{t('No bill records in this period')}</EmptyTitle>
            <EmptyDescription>
              {t('Try another period or adjust the current filters.')}
            </EmptyDescription>
          </EmptyHeader>
        </Empty>
      )}
      {!props.isLoading && currentPage && currentPage.items.length > 0 && (
        <>
          <Table
            className={cn(
              props.dimension === 'upstream_channel_model'
                ? 'min-w-[980px]'
                : 'min-w-[860px]'
            )}
          >
            <TableHeader>
              <TableRow>
                <TableHead>{t(primaryHeading(props.dimension))}</TableHead>
                {secondHeading ? (
                  <TableHead>{t(secondHeading)}</TableHead>
                ) : null}
                {thirdHeading ? <TableHead>{t(thirdHeading)}</TableHead> : null}
                <TableHead className='text-right'>{t('Records')}</TableHead>
                <TableHead className='text-right'>
                  {t('Input tokens')}
                </TableHead>
                <TableHead className='text-right'>
                  {t('Output tokens')}
                </TableHead>
                <TableHead className='text-right'>
                  {t('Total tokens')}
                </TableHead>
                <TableHead className='text-right'>
                  {props.perspective === 'customer'
                    ? t('Net billed quota')
                    : t('Routed billed quota')}
                </TableHead>
                <TableHead className='w-32 text-right'>
                  {t('Details')}
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody className='[&>tr]:h-12'>
              {currentPage.items.map((row) => (
                <TableRow key={`${props.dimension}:${row.key}`}>
                  <TableCell>
                    <span
                      className={cn(
                        props.dimension.startsWith('upstream_channel')
                          ? 'font-mono text-xs'
                          : 'font-medium'
                      )}
                    >
                      {props.dimension.startsWith('upstream_channel')
                        ? row.api_address || t('Unknown API address')
                        : row.label}
                    </span>
                    {props.dimension.startsWith('user') && row.user_id ? (
                      <span className='text-muted-foreground ml-2 text-xs'>
                        #{row.user_id}
                      </span>
                    ) : null}
                    {!props.dimension.startsWith('user') &&
                    !props.dimension.startsWith('upstream_channel') &&
                    row.channel_id ? (
                      <span className='text-muted-foreground ml-2 text-xs'>
                        #{row.channel_id}
                      </span>
                    ) : null}
                  </TableCell>
                  {secondHeading ? (
                    <TableCell>
                      <span
                        className={cn(
                          (props.dimension === 'user_model' ||
                            props.dimension === 'channel_model') &&
                            'font-mono text-xs'
                        )}
                      >
                        {secondaryLabel(row, props.dimension)}
                      </span>
                      {props.dimension !== 'user_model' &&
                      props.dimension !== 'channel_model' &&
                      row.channel_id ? (
                        <span className='text-muted-foreground ml-2 text-xs'>
                          #{row.channel_id}
                        </span>
                      ) : null}
                    </TableCell>
                  ) : null}
                  {thirdHeading ? (
                    <TableCell>
                      <span className='font-mono text-xs'>
                        {row.model_name || '-'}
                      </span>
                    </TableCell>
                  ) : null}
                  <TableCell className='text-right tabular-nums'>
                    {numberFormatter.format(row.record_count)}
                  </TableCell>
                  <TableCell className='text-right tabular-nums'>
                    {numberFormatter.format(row.prompt_tokens)}
                  </TableCell>
                  <TableCell className='text-right tabular-nums'>
                    {numberFormatter.format(row.completion_tokens)}
                  </TableCell>
                  <TableCell className='text-right tabular-nums'>
                    {numberFormatter.format(
                      row.prompt_tokens + row.completion_tokens
                    )}
                  </TableCell>
                  <TableCell className='text-right font-medium tabular-nums'>
                    {formatBillingAmountFromQuota(row.quota)}
                  </TableCell>
                  <TableCell className='text-right'>
                    <Button
                      type='button'
                      variant='ghost'
                      size='sm'
                      onClick={() => props.onOpenDetails(row)}
                    >
                      {t('View details')}
                      <HugeiconsIcon
                        icon={ArrowRight02Icon}
                        strokeWidth={2}
                        data-icon='inline-end'
                      />
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
          <div className='border-border flex items-center justify-between gap-3 border-t px-4 py-3'>
            <p className='text-muted-foreground text-sm'>
              {t('{{count}} groups', {
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
        </>
      )}
    </section>
  )
}
