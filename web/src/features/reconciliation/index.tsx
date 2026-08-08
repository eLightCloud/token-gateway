import {
  Download04Icon,
  InformationCircleIcon,
} from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useQuery } from '@tanstack/react-query'
import { getRouteApi, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { SectionPageLayout } from '@/components/layout'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from '@/components/ui/empty'
import dayjs from '@/lib/dayjs'

import { billKeys, downloadBillCSV, getBillGroups, getBillSummary } from './api'
import { BillDetailsDrawer } from './components/bill-details-drawer'
import { BillFilterBar, BillPeriodControls } from './components/bill-filter-bar'
import { BillGroupsTable } from './components/bill-groups'
import { BillSummaryPanel } from './components/bill-summary'
import {
  BILL_DEFAULT_PAGE_SIZE,
  BILL_DIMENSIONS,
  BILL_PERSPECTIVES,
} from './constants'
import type {
  BillDimension,
  BillFilters,
  BillGroupRow,
  BillPerspective,
  BillType,
} from './types'

const route = getRouteApi('/_authenticated/reconciliation/')

function defaultRange() {
  const now = dayjs()
  return {
    startTimestamp: now.startOf('month').unix(),
    endTimestamp: now.add(1, 'month').startOf('month').unix(),
  }
}

function defaultDimension(perspective: BillPerspective): BillDimension {
  if (perspective === 'customer') return 'user'
  if (perspective === 'api_address') return 'upstream_channel'
  return 'channel'
}

function QueryErrorState(props: { onRetry: () => void }) {
  const { t } = useTranslation()
  return (
    <Empty className='min-h-80'>
      <EmptyHeader>
        <EmptyMedia variant='icon'>
          <HugeiconsIcon
            icon={InformationCircleIcon}
            strokeWidth={2}
            aria-hidden='true'
          />
        </EmptyMedia>
        <EmptyTitle>{t('Unable to load token bill')}</EmptyTitle>
        <EmptyDescription>
          {t(
            'The billing query failed. No relay or billing operation was affected.'
          )}
        </EmptyDescription>
      </EmptyHeader>
      <Button type='button' variant='outline' onClick={props.onRetry}>
        {t('Retry')}
      </Button>
    </Empty>
  )
}

export function Reconciliation() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const search = route.useSearch()
  const range = defaultRange()
  const perspective = (search.perspective ?? 'customer') as BillPerspective
  const allowedDimensions = BILL_DIMENSIONS[perspective].map(
    (item) => item.value
  )
  const requestedDimension = search.dimension as BillDimension | undefined
  const dimension =
    requestedDimension && allowedDimensions.includes(requestedDimension)
      ? requestedDimension
      : defaultDimension(perspective)
  const filters: BillFilters = {
    startTimestamp: search.start ?? range.startTimestamp,
    endTimestamp: search.end ?? range.endTimestamp,
    perspective,
    dimension,
    billType: (search.billType ?? 'all') as BillType,
    organizationId: search.organizationId,
    modelName: search.modelName,
    channelId: search.channelId,
    apiAddress: undefined,
    requestId: search.requestId,
    page: search.page ?? 1,
    pageSize: BILL_DEFAULT_PAGE_SIZE,
  }
  const [isExporting, setIsExporting] = useState(false)
  const [selectedGroup, setSelectedGroup] = useState<BillGroupRow>()

  const summaryQuery = useQuery({
    queryKey: billKeys.summary(filters),
    queryFn: () => getBillSummary(filters),
  })
  const groupsQuery = useQuery({
    queryKey: billKeys.groups(filters),
    queryFn: () => getBillGroups(filters),
  })

  const updateFilters = (patch: Partial<BillFilters>) => {
    const next = { ...filters, ...patch }
    void navigate({
      to: '/reconciliation',
      search: {
        start: next.startTimestamp,
        end: next.endTimestamp,
        perspective:
          next.perspective === 'customer' ? undefined : next.perspective,
        dimension:
          next.dimension === defaultDimension(next.perspective)
            ? undefined
            : next.dimension,
        billType: next.billType === 'all' ? undefined : next.billType,
        organizationId: next.organizationId,
        modelName: next.modelName,
        channelId: next.channelId,
        requestId: next.requestId,
        page: next.page > 1 ? next.page : undefined,
      },
    })
  }

  const changePerspective = (nextPerspective: BillPerspective) => {
    setSelectedGroup(undefined)
    updateFilters({
      perspective: nextPerspective,
      dimension: defaultDimension(nextPerspective),
      billType: 'all',
      page: 1,
    })
  }

  const exportCurrentBill = async () => {
    setIsExporting(true)
    try {
      await downloadBillCSV(filters)
      toast.success(t('Token bill exported'))
    } catch {
      toast.error(t('Unable to export token bill'))
    } finally {
      setIsExporting(false)
    }
  }

  const retry = () => {
    void Promise.all([summaryQuery.refetch(), groupsQuery.refetch()])
  }

  let perspectiveDescription = t(
    'Usage grouped by each channel current API address'
  )
  let perspectiveNote = t(
    'API addresses use current channel configuration, not historical snapshots.'
  )
  if (perspective === 'customer') {
    perspectiveDescription = t('Customer billing and usage from database facts')
    perspectiveNote = t('Refunds reduce the customer net bill.')
  } else if (perspective === 'upstream') {
    perspectiveDescription = t('Channel and model usage from database facts')
    perspectiveNote = t(
      'Channel usage is recorded consumption, not provider cost.'
    )
  }

  return (
    <SectionPageLayout fixedContent>
      <SectionPageLayout.Title>{t('Token bill')}</SectionPageLayout.Title>
      <SectionPageLayout.Actions>
        <Button
          type='button'
          disabled={isExporting}
          onClick={() => void exportCurrentBill()}
        >
          <HugeiconsIcon
            icon={Download04Icon}
            strokeWidth={2}
            data-icon='inline-start'
          />
          {isExporting ? t('Exporting...') : t('Export current bill')}
        </Button>
      </SectionPageLayout.Actions>
      <SectionPageLayout.Content>
        <div className='flex h-full min-h-0 flex-col gap-3'>
          <div className='flex flex-wrap items-end justify-between gap-3'>
            <div>
              <p className='text-muted-foreground text-sm'>
                {perspectiveDescription}
              </p>
              <p className='text-muted-foreground mt-1 text-xs'>
                {t(
                  'This page is read-only and does not contact upstream providers.'
                )}
              </p>
            </div>
            <BillPeriodControls filters={filters} onChange={updateFilters} />
          </div>

          <div className='bg-background min-h-0 flex-1 overflow-auto rounded-xl border'>
            <div className='border-border flex flex-wrap items-center justify-between gap-3 border-b p-3'>
              <div className='border-border flex rounded-lg border p-0.5'>
                {BILL_PERSPECTIVES.map((item) => (
                  <Button
                    key={item.value}
                    type='button'
                    size='sm'
                    variant={perspective === item.value ? 'secondary' : 'ghost'}
                    aria-pressed={perspective === item.value}
                    onClick={() => changePerspective(item.value)}
                  >
                    {t(item.labelKey)}
                  </Button>
                ))}
              </div>
              <p className='text-muted-foreground text-xs'>{perspectiveNote}</p>
            </div>
            <BillFilterBar
              filters={filters}
              options={summaryQuery.data?.filter_options}
              onChange={updateFilters}
            />
            {summaryQuery.data && !summaryQuery.data.consume_logging_enabled ? (
              <Alert variant='destructive' className='m-3 w-auto'>
                <AlertDescription>
                  {t(
                    'Consumption logging is disabled. Bills may be incomplete.'
                  )}
                </AlertDescription>
              </Alert>
            ) : null}
            {summaryQuery.isError || groupsQuery.isError ? (
              <QueryErrorState onRetry={retry} />
            ) : (
              <>
                <BillSummaryPanel
                  summary={summaryQuery.data}
                  isLoading={summaryQuery.isLoading}
                />
                <BillGroupsTable
                  perspective={perspective}
                  dimension={dimension}
                  page={groupsQuery.data}
                  isLoading={groupsQuery.isLoading}
                  onDimensionChange={(nextDimension) => {
                    setSelectedGroup(undefined)
                    updateFilters({ dimension: nextDimension, page: 1 })
                  }}
                  onPageChange={(page) => updateFilters({ page })}
                  onOpenDetails={setSelectedGroup}
                />
              </>
            )}
          </div>
        </div>
        <BillDetailsDrawer
          filters={filters}
          dimension={dimension}
          selected={selectedGroup}
          onOpenChange={(open) => {
            if (!open) setSelectedGroup(undefined)
          }}
        />
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
