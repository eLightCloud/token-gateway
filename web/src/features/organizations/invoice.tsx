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
import { CalendarDays, Download, RefreshCw } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'
import { SectionPageLayout } from '@/components/layout'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
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
  TableFooter,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { useAuthStore } from '@/stores/auth-store'

import {
  downloadOrganizationExport,
  getOrganizationSelf,
  organizationKeys,
} from './api'
import {
  getBeijingInvoiceDefaultRange,
  getBeijingMonthDateRange,
} from './beijing-time'
import { FilterDraftNotice } from './filter-draft-notice'
import { buildOrganizationInvoiceExportUrl } from './invoice-api'
import { organizationInvoiceCategoryName } from './invoice-category'
import { OrganizationInvoiceFinancialSummary } from './invoice-financial-summary'
import { formatOrganizationInvoiceUSD } from './invoice-format'
import { organizationInvoiceNeedsExpenseReview } from './invoice-presentation'
import { OrganizationInvoiceReconciliation } from './invoice-reconciliation'
import type { OrganizationInvoice } from './invoice-types'
import { OrganizationRecalculateButton } from './organization-recalculate-button'
import { UsageDateRangeFields } from './usage-date-range'
import { useOrganizationInvoiceQuery } from './use-organization-invoice-query'
import { useUsageDateRange } from './use-usage-date-range'

type OrganizationInvoicePanelProps = {
  enabled?: boolean
  organizationId?: number
  currentOrganizationId?: number
}

function InvoiceTableSkeleton() {
  return (
    <div className='space-y-2 p-4'>
      <Skeleton className='h-10 w-full' />
      <Skeleton className='h-12 w-full' />
      <Skeleton className='h-12 w-full' />
      <Skeleton className='h-12 w-full' />
    </div>
  )
}

function InvoiceEmptyState() {
  const { t } = useTranslation()
  return (
    <Empty className='min-h-56'>
      <EmptyHeader>
        <EmptyMedia variant='icon'>
          <CalendarDays />
        </EmptyMedia>
        <EmptyTitle>{t('No invoice usage in this period')}</EmptyTitle>
        <EmptyDescription>
          {t('Try another billing period or refresh after usage is recorded.')}
        </EmptyDescription>
      </EmptyHeader>
    </Empty>
  )
}

function CategoryInvoiceTable(props: {
  invoice?: OrganizationInvoice
  isLoading: boolean
}) {
  const { t } = useTranslation()
  if (props.isLoading) return <InvoiceTableSkeleton />
  if (!props.invoice?.category_rows.length) return <InvoiceEmptyState />

  return (
    <div className='overflow-x-auto'>
      <Table className='min-w-max'>
        <TableHeader>
          <TableRow>
            <TableHead className='bg-background sticky left-0 z-10 min-w-40'>
              {t('Model category')}
            </TableHead>
            {props.invoice.accounts.map((account) => (
              <TableHead key={account.user_id} className='min-w-32 text-right'>
                <span className='block'>{account.username}</span>
                {account.display_name ? (
                  <span className='text-muted-foreground block font-normal'>
                    {account.display_name}
                  </span>
                ) : null}
              </TableHead>
            ))}
            <TableHead className='min-w-32 text-right'>
              {t('Consumption total')}
            </TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {props.invoice.category_rows.map((row) => {
            const amounts = new Map(
              row.account_amounts.map((amount) => [
                amount.user_id,
                amount.gross_amount_usd,
              ])
            )
            return (
              <TableRow key={row.category_key}>
                <TableCell className='bg-background sticky left-0 z-10 font-medium'>
                  <span>
                    {organizationInvoiceCategoryName(
                      row.category_key,
                      row.category_name,
                      row.fallback,
                      t
                    )}
                  </span>
                  <span className='text-muted-foreground mt-1 block max-w-56 truncate text-xs'>
                    {row.models.join(', ')}
                  </span>
                </TableCell>
                {props.invoice?.accounts.map((account) => (
                  <TableCell
                    key={account.user_id}
                    className='text-right tabular-nums'
                  >
                    {formatOrganizationInvoiceUSD(
                      amounts.get(account.user_id) ?? '0',
                      true
                    )}
                  </TableCell>
                ))}
                <TableCell className='text-right font-medium tabular-nums'>
                  {formatOrganizationInvoiceUSD(row.gross_amount_usd)}
                </TableCell>
              </TableRow>
            )
          })}
        </TableBody>
        <TableFooter>
          <TableRow>
            <TableCell className='bg-muted sticky left-0 z-10 font-semibold'>
              {t('Total')}
            </TableCell>
            {props.invoice.accounts.map((account) => (
              <TableCell
                key={account.user_id}
                className='text-right tabular-nums'
              >
                {formatOrganizationInvoiceUSD(account.gross_amount_usd)}
              </TableCell>
            ))}
            <TableCell className='text-right font-semibold tabular-nums'>
              {formatOrganizationInvoiceUSD(
                props.invoice.gross_total_amount_usd
              )}
            </TableCell>
          </TableRow>
        </TableFooter>
      </Table>
    </div>
  )
}

function ModelInvoiceTable(props: {
  invoice?: OrganizationInvoice
  isLoading: boolean
}) {
  const { t } = useTranslation()
  if (props.isLoading) return <InvoiceTableSkeleton />
  if (!props.invoice?.model_rows.length) return <InvoiceEmptyState />

  return (
    <div className='overflow-x-auto'>
      <Table className='min-w-max'>
        <TableHeader>
          <TableRow>
            <TableHead className='bg-background sticky left-0 z-10 min-w-56'>
              {t('Model')}
            </TableHead>
            {props.invoice.accounts.map((account) => (
              <TableHead key={account.user_id} className='min-w-32 text-right'>
                {account.username}
              </TableHead>
            ))}
            <TableHead className='min-w-32 text-right'>{t('Total')}</TableHead>
            <TableHead className='min-w-24 text-right'>{t('Share')}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {props.invoice.model_rows.map((row) => {
            const amounts = new Map(
              row.account_amounts.map((amount) => [
                amount.user_id,
                amount.gross_amount_usd,
              ])
            )
            return (
              <TableRow key={row.model_name}>
                <TableCell className='bg-background sticky left-0 z-10 font-medium'>
                  {row.model_name}
                </TableCell>
                {props.invoice?.accounts.map((account) => (
                  <TableCell
                    key={account.user_id}
                    className='text-right tabular-nums'
                  >
                    {formatOrganizationInvoiceUSD(
                      amounts.get(account.user_id) ?? '0',
                      true
                    )}
                  </TableCell>
                ))}
                <TableCell className='text-right font-medium tabular-nums'>
                  {formatOrganizationInvoiceUSD(row.gross_amount_usd)}
                </TableCell>
                <TableCell className='text-right tabular-nums'>
                  {row.share_percent}%
                </TableCell>
              </TableRow>
            )
          })}
        </TableBody>
        <TableFooter>
          <TableRow>
            <TableCell className='bg-muted sticky left-0 z-10 font-semibold'>
              {t('Total')}
            </TableCell>
            {props.invoice.accounts.map((account) => (
              <TableCell
                key={account.user_id}
                className='text-right tabular-nums'
              >
                {formatOrganizationInvoiceUSD(account.gross_amount_usd)}
              </TableCell>
            ))}
            <TableCell className='text-right font-semibold tabular-nums'>
              {formatOrganizationInvoiceUSD(
                props.invoice.gross_total_amount_usd
              )}
            </TableCell>
            <TableCell className='text-right'>100.0%</TableCell>
          </TableRow>
        </TableFooter>
      </Table>
    </div>
  )
}

const organizationInvoiceBuildStageLabels: Record<string, string> = {
  pending: 'Waiting for the invoice builder',
  baseline: 'Preparing opening balances',
  usage: 'Aggregating model usage',
  financials: 'Computing fund flows',
  publish: 'Publishing snapshot',
}

function organizationInvoiceBuildStageLabel(stage: string): string {
  return (
    organizationInvoiceBuildStageLabels[stage] ?? 'Building invoice snapshot'
  )
}

export function OrganizationInvoicePanel(props: OrganizationInvoicePanelProps) {
  const userId = useAuthStore((state) => state.auth.user?.id)
  return (
    <OrganizationInvoiceContent
      key={`${props.organizationId ?? props.currentOrganizationId}:${userId}`}
      {...props}
    />
  )
}

function OrganizationInvoiceContent(props: OrganizationInvoicePanelProps) {
  const { t } = useTranslation()
  const authenticatedUserId = useAuthStore((state) => state.auth.user?.id)
  // Default snapshot range: month start through yesterday (Beijing), or the
  // previous full month on the first day of a month when no complete day
  // exists yet.
  const currentRange = getBeijingInvoiceDefaultRange()
  const previousRange = getBeijingMonthDateRange(Date.now(), -1)
  const dateRange = useUsageDateRange({
    initialRange: currentRange,
    required: true,
  })
  const submitted = useOrganizationInvoiceQuery({
    ...props,
    authenticatedUserId,
    initialParams: {
      start_date: currentRange.startDate,
      end_date: currentRange.endDate,
    },
    dateRange,
  })
  const { invoiceQuery, params } = submitted
  const invoice = invoiceQuery.data?.data
  const expenseNeedsReview = organizationInvoiceNeedsExpenseReview(invoice)
  const invoiceGenerating = invoice?.generation_status === 'generating'
  const invoiceLoading =
    invoiceQuery.isLoading ||
    (invoiceGenerating && !invoice?.previous_publication)
  const sourceAsOf = invoice?.source_as_of
    ? new Intl.DateTimeFormat(undefined, {
        dateStyle: 'medium',
        timeStyle: 'medium',
        timeZone: 'Asia/Shanghai',
      }).format((invoice.evidence_as_of || invoice.source_as_of) * 1000)
    : undefined
  const invalidRange = !dateRange.isDateRangeValid
  let invoiceError: string | undefined
  if (invoiceQuery.error instanceof Error) {
    invoiceError = invoiceQuery.error.message
  } else if (invoiceQuery.data && !invoiceQuery.data.success) {
    invoiceError = invoiceQuery.data.message || t('Failed to load')
  }

  const selectRange = (range: { startDate: string; endDate: string }) => {
    dateRange.setStartDate(range.startDate)
    dateRange.setEndDate(range.endDate)
  }

  return (
    <div className='space-y-4'>
      <Card>
        <CardContent className='flex flex-col gap-3 sm:flex-row sm:flex-wrap sm:items-end sm:justify-between'>
          <div className='flex flex-1 flex-wrap items-end gap-2'>
            <UsageDateRangeFields
              range={dateRange}
              className='grid w-full grid-cols-2 gap-2 sm:w-96 sm:shrink-0'
            />
            <Button variant='outline' onClick={() => selectRange(currentRange)}>
              {t('Current month')}
            </Button>
            <Button
              variant='outline'
              onClick={() => selectRange(previousRange)}
            >
              {t('Previous month')}
            </Button>
          </div>
          <div className='flex flex-wrap gap-2'>
            <Button
              variant='outline'
              disabled={invalidRange || submitted.isRefreshing}
              onClick={submitted.refresh}
              aria-busy={submitted.isRefreshing}
            >
              <RefreshCw data-icon='inline-start' />
              {t('Query')}
            </Button>
            <OrganizationRecalculateButton
              kind='invoice'
              coveredUntil={invoice?.source_as_of}
              organizationId={
                props.organizationId ?? props.currentOrganizationId
              }
              scope={`${params.start_date} – ${params.end_date}`}
              pending={submitted.isRecalculating}
              disabled={
                invalidRange ||
                submitted.hasChanges ||
                submitted.isRefreshing ||
                invoiceGenerating ||
                !invoice?.recalculation_token
              }
              onConfirm={submitted.recalculate}
            />
            <Button
              variant='outline'
              disabled={
                invalidRange ||
                submitted.hasChanges ||
                submitted.isRefreshing ||
                invoiceGenerating ||
                !invoice
              }
              onClick={() =>
                void downloadOrganizationExport(
                  buildOrganizationInvoiceExportUrl(
                    params,
                    props.organizationId
                  )
                )
              }
            >
              <Download data-icon='inline-start' />
              {t('Export CSV')}
            </Button>
          </div>
        </CardContent>
        {submitted.hasChanges ? (
          <CardContent className='pt-0'>
            <FilterDraftNotice visible />
          </CardContent>
        ) : null}
        {currentRange.currentMonthPending ? (
          <CardContent className='pt-0'>
            <p className='text-muted-foreground text-xs'>
              {t(
                'The current month has no completed day yet; the previous month invoice is shown.'
              )}
            </p>
          </CardContent>
        ) : null}
      </Card>

      {invoiceError ? (
        <Card>
          <ErrorState
            title={t('Failed to load organization invoice')}
            description={invoiceError}
            onRetry={submitted.retry}
            className='min-h-64'
          />
        </Card>
      ) : (
        <>
          {invoiceGenerating ? (
            <Card>
              <CardContent className='text-muted-foreground space-y-1 py-10 text-center text-sm'>
                <p>
                  {invoice?.build?.period?.start_date &&
                  invoice?.build?.period?.end_date
                    ? t('Building {{start}} – {{end}} invoice', {
                        start: invoice.build.period.start_date,
                        end: invoice.build.period.end_date,
                      })
                    : t('Generating...')}
                  {invoice?.previous_publication
                    ? ` · ${t('Showing the last published result')}`
                    : ''}
                </p>
                <p>{t('Use Query to check the latest progress.')}</p>
                {invoice?.build?.stage ? (
                  <p className='text-xs'>
                    {t(organizationInvoiceBuildStageLabel(invoice.build.stage))}
                    {invoice.build.progress
                      ? ` · ${invoice.build.progress}%`
                      : ''}
                  </p>
                ) : null}
              </CardContent>
            </Card>
          ) : null}
          <OrganizationInvoiceReconciliation invoice={invoice} />
          <div className='grid gap-4 md:grid-cols-2'>
            <Card size='sm'>
              <CardHeader>
                <CardDescription>
                  {t(
                    expenseNeedsReview
                      ? 'Confirmed usage subtotal'
                      : 'Consumption total'
                  )}
                </CardDescription>
                <CardTitle className='text-2xl tabular-nums'>
                  {invoiceLoading ? (
                    <Skeleton className='h-8 w-28' />
                  ) : (
                    `$${formatOrganizationInvoiceUSD(invoice?.gross_total_amount_usd ?? '0')}`
                  )}
                </CardTitle>
              </CardHeader>
            </Card>
            <Card size='sm'>
              <CardHeader>
                <CardDescription>
                  {t('Billing period (Beijing time)')}
                </CardDescription>
                <CardTitle className='text-base'>
                  {invoice?.period.start_date ?? params.start_date} —{' '}
                  {invoice?.period.end_date ?? params.end_date}
                </CardTitle>
                {sourceAsOf ? (
                  <p className='text-muted-foreground text-xs'>
                    {t('Updated at')}: {sourceAsOf}
                  </p>
                ) : null}
              </CardHeader>
            </Card>
          </div>

          <Card>
            <CardHeader>
              <CardTitle>{t('Account financial summary')}</CardTitle>
              <CardDescription>
                {t(
                  'All billing-period organization accounts, including accounts with no usage, are shown.'
                )}
              </CardDescription>
            </CardHeader>
            <CardContent className='px-0'>
              <OrganizationInvoiceFinancialSummary
                invoice={invoice}
                isLoading={invoiceLoading}
              />
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>{t('Model category consumption summary')}</CardTitle>
            </CardHeader>
            <CardContent className='px-0'>
              <CategoryInvoiceTable
                invoice={invoice}
                isLoading={invoiceLoading}
              />
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>{t('AI model usage summary')}</CardTitle>
              <CardDescription>
                {t(
                  'Amounts are grouped by account and full model name in USD.'
                )}
              </CardDescription>
            </CardHeader>
            <CardContent className='px-0'>
              <ModelInvoiceTable invoice={invoice} isLoading={invoiceLoading} />
            </CardContent>
          </Card>
        </>
      )}
    </div>
  )
}

export function OrganizationInvoicePage() {
  const { t } = useTranslation()
  const selfQuery = useQuery({
    queryKey: organizationKeys.self,
    queryFn: getOrganizationSelf,
  })
  const self = selfQuery.data?.data
  let selfError: string | undefined
  if (selfQuery.error instanceof Error) {
    selfError = selfQuery.error.message
  } else if (selfQuery.data && !selfQuery.data.success) {
    selfError = selfQuery.data.message || t('Failed to load')
  }

  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>
        {t('Organization invoice')}
      </SectionPageLayout.Title>
      <SectionPageLayout.Content>
        {selfQuery.isLoading ? <InvoiceTableSkeleton /> : null}
        {!selfQuery.isLoading && selfError ? (
          <ErrorState
            title={t('Failed to load organization invoice')}
            description={selfError}
            onRetry={() => {
              void selfQuery.refetch()
            }}
            className='min-h-80'
          />
        ) : null}
        {!selfQuery.isLoading && !selfError && self?.member.role !== 'admin' ? (
          <Empty className='min-h-80'>
            <EmptyHeader>
              <EmptyTitle>{t('Organization admin access required')}</EmptyTitle>
              <EmptyDescription>
                {t('Only organization admins can view and configure invoices.')}
              </EmptyDescription>
            </EmptyHeader>
          </Empty>
        ) : null}
        {!selfError && self?.member.role === 'admin' ? (
          <OrganizationInvoicePanel
            currentOrganizationId={self.organization.id}
          />
        ) : null}
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
