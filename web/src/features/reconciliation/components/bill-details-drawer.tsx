import { useQuery } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyTitle,
} from '@/components/ui/empty'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'

import { billKeys, getBillEntries, getBillGroups } from '../api'
import { billDetailBreakdownDimension, filtersForBillGroup } from '../grouping'
import type { BillDimension, BillFilters, BillGroupRow } from '../types'
import { BillDetailBreakdown } from './bill-detail-breakdown'
import { BillEntriesTable } from './bill-entries'

export function BillDetailsDrawer(props: {
  filters: BillFilters
  dimension: BillDimension
  selected?: BillGroupRow
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)
  const [breakdownPage, setBreakdownPage] = useState(1)

  useEffect(() => {
    setPage(1)
    setBreakdownPage(1)
  }, [props.selected?.key, props.dimension])

  const selectedFilters = props.selected
    ? filtersForBillGroup(props.filters, props.selected)
    : props.filters
  const detailFilters: BillFilters = { ...selectedFilters, page }
  const breakdownDimension = billDetailBreakdownDimension(props.dimension)
  const breakdownFilters: BillFilters = {
    ...selectedFilters,
    dimension: breakdownDimension ?? props.dimension,
    page: breakdownPage,
    pageSize: 10,
  }
  const detailsQuery = useQuery({
    queryKey: billKeys.entries(detailFilters),
    queryFn: () => getBillEntries(detailFilters),
    enabled: Boolean(props.selected),
  })
  const breakdownQuery = useQuery({
    queryKey: billKeys.groups(breakdownFilters),
    queryFn: () => getBillGroups(breakdownFilters),
    enabled: Boolean(props.selected && breakdownDimension),
  })

  let selectedLabel = props.selected?.label || ''
  if (props.dimension === 'user_model') {
    selectedLabel += ` / ${props.selected?.model_name || '-'}`
  } else if (props.dimension === 'user_channel') {
    selectedLabel += ` / ${props.selected?.channel_name || '-'}`
  } else if (props.dimension === 'channel_model') {
    selectedLabel += ` / ${props.selected?.model_name || '-'}`
  } else if (props.dimension === 'upstream_channel') {
    selectedLabel = `${props.selected?.api_address || t('Unknown API address')} / ${props.selected?.channel_name || '-'}`
  } else if (props.dimension === 'upstream_channel_model') {
    selectedLabel = `${props.selected?.api_address || t('Unknown API address')} / ${props.selected?.channel_name || '-'} / ${props.selected?.model_name || '-'}`
  }

  return (
    <Sheet open={Boolean(props.selected)} onOpenChange={props.onOpenChange}>
      <SheetContent className='w-[96vw] sm:max-w-6xl'>
        <SheetHeader className='border-border border-b pr-12'>
          <SheetTitle>
            {t('Bill details')}: {selectedLabel}
          </SheetTitle>
          <SheetDescription>
            {t(
              'These records use the same period, perspective, and filters as the overview.'
            )}
          </SheetDescription>
        </SheetHeader>
        <div className='min-h-0 flex-1 overflow-auto'>
          {detailsQuery.isError || breakdownQuery.isError ? (
            <Empty className='min-h-72'>
              <EmptyHeader>
                <EmptyTitle>{t('Unable to load token bill')}</EmptyTitle>
                <EmptyDescription>
                  {t(
                    'The billing query failed. No relay or billing operation was affected.'
                  )}
                </EmptyDescription>
              </EmptyHeader>
              <Button
                type='button'
                variant='outline'
                onClick={() =>
                  void Promise.all([
                    detailsQuery.refetch(),
                    breakdownQuery.refetch(),
                  ])
                }
              >
                {t('Retry')}
              </Button>
            </Empty>
          ) : (
            <>
              {breakdownDimension ? (
                <BillDetailBreakdown
                  dimension={breakdownDimension}
                  page={breakdownQuery.data}
                  isLoading={breakdownQuery.isLoading}
                  onPageChange={setBreakdownPage}
                />
              ) : null}
              <BillEntriesTable
                page={detailsQuery.data}
                isLoading={detailsQuery.isLoading}
                onPageChange={setPage}
              />
            </>
          )}
        </div>
      </SheetContent>
    </Sheet>
  )
}
