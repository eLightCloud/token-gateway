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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { isAxiosError } from 'axios'
import type { TFunction } from 'i18next'
import { CalendarDays, Download, RefreshCw, Settings2 } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { ErrorState } from '@/components/error-state'
import { SectionPageLayout } from '@/components/layout'
import { Badge } from '@/components/ui/badge'
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
import { Input } from '@/components/ui/input'
import { ScrollArea } from '@/components/ui/scroll-area'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
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
import { getBeijingMonthDateRange } from './beijing-time'
import {
  buildOrganizationInvoiceExportUrl,
  getOrganizationInvoice,
  getOrganizationSettlementRules,
  organizationInvoiceKeys,
  updateOrganizationSettlementRule,
} from './invoice-api'
import type {
  OrganizationInvoice,
  OrganizationInvoiceAccountAmount,
  OrganizationInvoiceParams,
  OrganizationSettlementRuleOption,
  OrganizationSettlementRuleUpdatePayload,
} from './invoice-types'

type OrganizationInvoicePanelProps = {
  organizationId?: number
  currentOrganizationId?: number
}

type SettlementRuleRowProps = {
  rule: OrganizationSettlementRuleOption
  isPending: boolean
  onSave: (rule: OrganizationSettlementRuleOption, factor: string) => void
}

const USD_FORMATTER = new Intl.NumberFormat('en-US', {
  minimumFractionDigits: 4,
  maximumFractionDigits: 4,
})

function formatUSD(value: string, emptyForZero = false): string {
  const amount = Number(value)
  if (!Number.isFinite(amount)) return '-'
  if (emptyForZero && amount === 0) return '-'
  return USD_FORMATTER.format(amount)
}

function organizationInvoiceCategoryName(
  categoryKey: string,
  categoryName: string,
  fallback: boolean,
  t: TFunction
): string {
  if (fallback) return categoryName
  switch (categoryKey) {
    case 'claude':
      return t('Claude')
    case 'gpt':
      return t('GPT')
    case 'gemini':
      return t('Gemini')
    case 'minimax':
      return t('MiniMax')
    case 'deepseek':
      return t('Deepseek')
    case 'kimi':
      return t('Kimi')
    default:
      return categoryName
  }
}

function factorLabel(
  rule: OrganizationInvoice['category_rows'][number]
): string {
  if (!rule.multiple_factors) return rule.factor
  return rule.factor_segments
    .map((segment) => `${segment.period_month}: ${segment.factor}`)
    .join(', ')
}

function accountAmount(
  amounts: OrganizationInvoiceAccountAmount[],
  userId: number
): string {
  return (
    amounts.find((amount) => amount.user_id === userId)?.gross_amount_usd ?? '0'
  )
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
              {t('Gross total')}
            </TableHead>
            <TableHead className='min-w-36 text-right'>
              {t('Settlement factor')}
            </TableHead>
            <TableHead className='min-w-32 text-right'>
              {t('Settled amount')}
            </TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {props.invoice.category_rows.map((row) => (
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
                  {formatUSD(
                    accountAmount(row.account_amounts, account.user_id),
                    true
                  )}
                </TableCell>
              ))}
              <TableCell className='text-right font-medium tabular-nums'>
                {formatUSD(row.gross_amount_usd)}
              </TableCell>
              <TableCell className='text-right tabular-nums'>
                {row.factor === '0.0000' ? (
                  <Badge variant='secondary'>{t('Free / exempt')}</Badge>
                ) : (
                  factorLabel(row)
                )}
              </TableCell>
              <TableCell className='text-right font-medium tabular-nums'>
                {formatUSD(row.settled_amount_usd)}
              </TableCell>
            </TableRow>
          ))}
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
                {formatUSD(account.gross_amount_usd)}
              </TableCell>
            ))}
            <TableCell className='text-right font-semibold tabular-nums'>
              {formatUSD(props.invoice.gross_total_amount_usd)}
            </TableCell>
            <TableCell className='text-right'>—</TableCell>
            <TableCell className='text-right font-semibold tabular-nums'>
              {formatUSD(props.invoice.settled_total_amount_usd)}
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
          {props.invoice.model_rows.map((row) => (
            <TableRow key={row.model_name}>
              <TableCell className='bg-background sticky left-0 z-10 font-medium'>
                {row.model_name}
              </TableCell>
              {props.invoice?.accounts.map((account) => (
                <TableCell
                  key={account.user_id}
                  className='text-right tabular-nums'
                >
                  {formatUSD(
                    accountAmount(row.account_amounts, account.user_id),
                    true
                  )}
                </TableCell>
              ))}
              <TableCell className='text-right font-medium tabular-nums'>
                {formatUSD(row.gross_amount_usd)}
              </TableCell>
              <TableCell className='text-right tabular-nums'>
                {row.share_percent}%
              </TableCell>
            </TableRow>
          ))}
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
                {formatUSD(account.gross_amount_usd)}
              </TableCell>
            ))}
            <TableCell className='text-right font-semibold tabular-nums'>
              {formatUSD(props.invoice.gross_total_amount_usd)}
            </TableCell>
            <TableCell className='text-right'>100.0%</TableCell>
          </TableRow>
        </TableFooter>
      </Table>
    </div>
  )
}

function SettlementRuleRow(props: SettlementRuleRowProps) {
  const { t } = useTranslation()
  const [factor, setFactor] = useState(props.rule.factor)
  const [confirmZero, setConfirmZero] = useState(false)
  const [confirmDefault, setConfirmDefault] = useState(false)
  const numericFactor = Number(factor)
  const factorValid =
    /^\d+(?:\.\d{1,4})?$/.test(factor) &&
    Number.isFinite(numericFactor) &&
    numericFactor >= 0 &&
    numericFactor <= 10
  const changed = factorValid && factor !== props.rule.factor

  const save = () => {
    if (!changed) return
    if (numericFactor === 0) {
      setConfirmZero(true)
      return
    }
    props.onSave(props.rule, factor)
  }

  return (
    <div className='space-y-3 rounded-lg border p-4'>
      <div className='flex items-start justify-between gap-3'>
        <div className='min-w-0'>
          <p className='font-medium'>
            {organizationInvoiceCategoryName(
              props.rule.category_key,
              props.rule.category_name,
              props.rule.fallback,
              t
            )}
          </p>
          <p className='text-muted-foreground mt-1 text-xs break-words'>
            {props.rule.models.join(', ')}
          </p>
        </div>
        {props.rule.inherited ? (
          <Badge variant='outline'>{t('Inherited')}</Badge>
        ) : (
          <Badge variant='secondary'>v{props.rule.version}</Badge>
        )}
      </div>
      <div className='flex flex-wrap items-end gap-2'>
        <label className='min-w-36 flex-1 space-y-1'>
          <span className='text-muted-foreground text-xs'>
            {t('Settlement factor')}
          </span>
          <Input
            inputMode='decimal'
            value={factor}
            aria-invalid={!factorValid}
            onChange={(event) => setFactor(event.target.value)}
          />
        </label>
        <Button
          variant='outline'
          disabled={props.isPending || factor === '1.0000'}
          onClick={() => setConfirmDefault(true)}
        >
          {t('Use default')}
        </Button>
        <Button disabled={props.isPending || !changed} onClick={save}>
          {t('Save')}
        </Button>
      </div>
      {!factorValid ? (
        <p className='text-destructive text-xs'>
          {t('Enter a factor from 0.0000 to 10.0000 with up to 4 decimals.')}
        </p>
      ) : null}
      {props.rule.source_effective_month ? (
        <p className='text-muted-foreground text-xs'>
          {t('Source effective month')}: {props.rule.source_effective_month}
        </p>
      ) : null}
      <ConfirmDialog
        open={confirmZero}
        onOpenChange={setConfirmZero}
        title={t('Confirm free settlement')}
        desc={t(
          'A zero factor makes this category free or exempt for the selected month. Personal usage charges are not changed.'
        )}
        confirmText={t('Set factor to zero')}
        isLoading={props.isPending}
        handleConfirm={() => {
          setConfirmZero(false)
          props.onSave(props.rule, factor)
        }}
      />
      <ConfirmDialog
        open={confirmDefault}
        onOpenChange={setConfirmDefault}
        title={t('Confirm default settlement factor')}
        desc={t(
          'Set this category to the default factor 1.0000 starting in {{month}}?',
          { month: props.rule.effective_month }
        )}
        confirmText={t('Use default')}
        isLoading={props.isPending}
        handleConfirm={() => {
          setConfirmDefault(false)
          setFactor('1.0000')
          props.onSave(props.rule, '1.0000')
        }}
      />
    </div>
  )
}

function SettlementRulesSheet(props: {
  open: boolean
  onOpenChange: (open: boolean) => void
  organizationId?: number
  currentOrganizationId?: number
  authenticatedUserId?: number
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const currentMonth = getBeijingMonthDateRange().effectiveMonth
  const [effectiveMonth, setEffectiveMonth] = useState(currentMonth)
  const effectiveMonthValid = /^\d{4}-(?:0[1-9]|1[0-2])$/.test(effectiveMonth)
  const scope = {
    mode: props.organizationId ? ('admin' as const) : ('current' as const),
    organizationId: props.organizationId ?? props.currentOrganizationId,
    authenticatedUserId: props.authenticatedUserId,
  }
  const rulesQuery = useQuery({
    queryKey: organizationInvoiceKeys.rules(scope, effectiveMonth),
    queryFn: () =>
      getOrganizationSettlementRules(effectiveMonth, props.organizationId),
    enabled:
      props.open && Boolean(props.authenticatedUserId) && effectiveMonthValid,
  })
  const updateMutation = useMutation({
    mutationFn: (payload: OrganizationSettlementRuleUpdatePayload) =>
      updateOrganizationSettlementRule(payload, props.organizationId),
    onSuccess: (response) => {
      if (!response.success) return
      toast.success(t('Settlement factor updated'))
      void queryClient.invalidateQueries({
        queryKey: organizationInvoiceKeys.rules(scope, effectiveMonth),
      })
      void queryClient.invalidateQueries({
        queryKey: organizationInvoiceKeys.scope(scope),
      })
    },
    onError: (error) => {
      if (isAxiosError(error) && error.response?.status === 409) {
        void queryClient.invalidateQueries({
          queryKey: organizationInvoiceKeys.rules(scope, effectiveMonth),
        })
      }
    },
  })
  let rulesError: string | undefined
  if (rulesQuery.error instanceof Error) {
    rulesError = rulesQuery.error.message
  } else if (rulesQuery.data && !rulesQuery.data.success) {
    rulesError = rulesQuery.data.message || t('Failed to load')
  }

  return (
    <Sheet open={props.open} onOpenChange={props.onOpenChange}>
      <SheetContent className='w-full sm:max-w-2xl'>
        <SheetHeader className='border-b'>
          <SheetTitle>{t('Settlement factor settings')}</SheetTitle>
          <SheetDescription>
            {t(
              'Factors are organization-only and affect invoice display and export, not personal charges.'
            )}
          </SheetDescription>
        </SheetHeader>
        <div className='px-4'>
          <label className='block max-w-56 space-y-1'>
            <span className='text-muted-foreground text-xs'>
              {t('Effective month (Beijing time)')}
            </span>
            <Input
              type='month'
              value={effectiveMonth}
              onChange={(event) => setEffectiveMonth(event.target.value)}
            />
            {effectiveMonthValid && effectiveMonth < currentMonth ? (
              <span className='text-warning block text-xs'>
                {t(
                  'Changing a historical month recalculates historical organization invoices.'
                )}
              </span>
            ) : null}
          </label>
        </div>
        <ScrollArea className='min-h-0 flex-1'>
          <div className='space-y-3 px-4 pb-6'>
            {rulesQuery.isLoading ? <InvoiceTableSkeleton /> : null}
            {rulesError ? (
              <ErrorState
                title={t('Failed to load settlement factors')}
                description={rulesError}
                onRetry={() => {
                  void rulesQuery.refetch()
                }}
                className='min-h-56'
              />
            ) : null}
            {!rulesError &&
              rulesQuery.data?.data?.map((rule) => (
                <SettlementRuleRow
                  key={`${rule.category_key}-${rule.effective_month}-${rule.version}-${rule.factor}`}
                  rule={rule}
                  isPending={updateMutation.isPending}
                  onSave={(selectedRule, factor) =>
                    updateMutation.mutate({
                      category_key: selectedRule.category_key,
                      factor,
                      effective_month: effectiveMonth,
                      expected_version: selectedRule.version,
                    })
                  }
                />
              ))}
            {!rulesError &&
            !rulesQuery.isLoading &&
            !rulesQuery.data?.data?.length ? (
              <p className='text-muted-foreground py-12 text-center text-sm'>
                {t('No used model categories are available to configure.')}
              </p>
            ) : null}
          </div>
        </ScrollArea>
      </SheetContent>
    </Sheet>
  )
}

export function OrganizationInvoicePanel(props: OrganizationInvoicePanelProps) {
  const { t } = useTranslation()
  const authenticatedUserId = useAuthStore((state) => state.auth.user?.id)
  const currentRange = getBeijingMonthDateRange()
  const previousRange = getBeijingMonthDateRange(Date.now(), -1)
  const [draftStartDate, setDraftStartDate] = useState(currentRange.startDate)
  const [draftEndDate, setDraftEndDate] = useState(currentRange.endDate)
  const [params, setParams] = useState<OrganizationInvoiceParams>({
    start_date: currentRange.startDate,
    end_date: currentRange.endDate,
  })
  const [settingsOpen, setSettingsOpen] = useState(false)
  const scope = {
    mode: props.organizationId ? ('admin' as const) : ('current' as const),
    organizationId: props.organizationId ?? props.currentOrganizationId,
    authenticatedUserId,
  }
  const invoiceQuery = useQuery({
    queryKey: organizationInvoiceKeys.invoice(scope, params),
    queryFn: () => getOrganizationInvoice(params, props.organizationId),
    enabled: Boolean(authenticatedUserId),
  })
  const invoice = invoiceQuery.data?.data
  const invalidRange =
    !draftStartDate || !draftEndDate || draftStartDate > draftEndDate
  let invoiceError: string | undefined
  if (invoiceQuery.error instanceof Error) {
    invoiceError = invoiceQuery.error.message
  } else if (invoiceQuery.data && !invoiceQuery.data.success) {
    invoiceError = invoiceQuery.data.message || t('Failed to load')
  }

  const applyRange = (range: { startDate: string; endDate: string }) => {
    setDraftStartDate(range.startDate)
    setDraftEndDate(range.endDate)
    setParams({
      start_date: range.startDate,
      end_date: range.endDate,
    })
  }

  return (
    <div className='space-y-4'>
      <Card>
        <CardContent className='flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between'>
          <div className='flex flex-1 flex-wrap items-end gap-2'>
            <label className='space-y-1'>
              <span className='text-muted-foreground text-xs'>
                {t('Start date')}
              </span>
              <Input
                type='date'
                value={draftStartDate}
                onChange={(event) => setDraftStartDate(event.target.value)}
              />
            </label>
            <label className='space-y-1'>
              <span className='text-muted-foreground text-xs'>
                {t('End date')}
              </span>
              <Input
                type='date'
                value={draftEndDate}
                onChange={(event) => setDraftEndDate(event.target.value)}
              />
            </label>
            <Button variant='outline' onClick={() => applyRange(currentRange)}>
              {t('Current month')}
            </Button>
            <Button variant='outline' onClick={() => applyRange(previousRange)}>
              {t('Previous month')}
            </Button>
            <Button
              disabled={invalidRange}
              onClick={() =>
                setParams({
                  start_date: draftStartDate,
                  end_date: draftEndDate,
                })
              }
            >
              {t('Apply')}
            </Button>
          </div>
          <div className='flex flex-wrap gap-2'>
            <Button
              variant='outline'
              onClick={() => void invoiceQuery.refetch()}
            >
              <RefreshCw data-icon='inline-start' />
              {t('Refresh')}
            </Button>
            <Button
              variant='outline'
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
            <Button onClick={() => setSettingsOpen(true)}>
              <Settings2 data-icon='inline-start' />
              {t('Configure factors')}
            </Button>
          </div>
        </CardContent>
      </Card>

      {invoiceError ? (
        <Card>
          <ErrorState
            title={t('Failed to load organization invoice')}
            description={invoiceError}
            onRetry={() => {
              void invoiceQuery.refetch()
            }}
            className='min-h-64'
          />
        </Card>
      ) : (
        <>
          <div className='grid gap-4 md:grid-cols-3'>
            <Card size='sm'>
              <CardHeader>
                <CardDescription>{t('Gross amount')}</CardDescription>
                <CardTitle className='text-2xl tabular-nums'>
                  ${formatUSD(invoice?.gross_total_amount_usd ?? '0')}
                </CardTitle>
              </CardHeader>
            </Card>
            <Card size='sm'>
              <CardHeader>
                <CardDescription>{t('Settled amount')}</CardDescription>
                <CardTitle className='text-2xl tabular-nums'>
                  ${formatUSD(invoice?.settled_total_amount_usd ?? '0')}
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
              </CardHeader>
            </Card>
          </div>

          <Card>
            <CardHeader>
              <CardTitle>{t('Model category settlement summary')}</CardTitle>
              <CardDescription>
                {t(
                  'Account columns show gross USD amounts. Settlement factors apply only to category totals.'
                )}
              </CardDescription>
            </CardHeader>
            <CardContent className='px-0'>
              <CategoryInvoiceTable
                invoice={invoice}
                isLoading={invoiceQuery.isLoading}
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
              <ModelInvoiceTable
                invoice={invoice}
                isLoading={invoiceQuery.isLoading}
              />
            </CardContent>
          </Card>
        </>
      )}

      <SettlementRulesSheet
        open={settingsOpen}
        onOpenChange={setSettingsOpen}
        organizationId={props.organizationId}
        currentOrganizationId={props.currentOrganizationId}
        authenticatedUserId={authenticatedUserId}
      />
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
