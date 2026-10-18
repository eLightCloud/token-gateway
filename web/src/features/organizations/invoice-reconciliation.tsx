import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'

import { formatOrganizationInvoiceUSD } from './invoice-format'
import {
  formatOrganizationInvoiceCoverage,
  getOrganizationInvoiceReview,
} from './invoice-presentation'
import type { OrganizationInvoice } from './invoice-types'

// Only unresolved evidence is listed. Normal wallet replay is not an error.
export function OrganizationInvoiceReconciliation(props: {
  invoice?: OrganizationInvoice
}) {
  const { t, i18n } = useTranslation()
  if (!props.invoice || props.invoice.generation_status === 'generating') {
    return null
  }
  const invoice = props.invoice
  const review = getOrganizationInvoiceReview(invoice)
  const coverage = invoice.expense_coverage
  const pendingTasks = coverage?.pending_tasks ?? 0
  const unknownRows = coverage?.unknown_rows ?? 0
  const pendingSourceRows = coverage?.pending_source_rows ?? 0
  const range = formatOrganizationInvoiceCoverage(invoice.coverage_period)
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const accounts = new Map(
    invoice.accounts.map((account) => [account.user_id, account])
  )
  const reasons: Record<string, string> = {
    final_expense_evidence_missing: t('Final expense is not confirmed'),
    expense_origin_missing: t('Consumption attribution evidence is missing'),
    consume_log_pending: t('Consumption record is awaiting delivery'),
    consume_history_cleared: t(
      'Historical consumption records have been cleared'
    ),
    log_source_snapshot_unverified: t(
      'Consumption source snapshot needs review'
    ),
    consume_logging_disabled: t('Consumption logging is disabled'),
    expense_coverage_missing: t(
      'Consumption completeness evidence is unavailable'
    ),
    expense_evidence_incomplete: t(
      'Some consumption evidence still needs review'
    ),
    task_funding_pending: t('Task funding is pending'),
    original_task_charge_missing: t('Original task charge is missing'),
    task_cash_flow_mismatch: t('Task charge and refund records do not match'),
    wallet_information_unavailable: t('Wallet information is unavailable'),
    wallet_evidence_incomplete: t('Wallet flow evidence is incomplete'),
  }
  return (
    <div className='space-y-3'>
      <p className='text-muted-foreground text-sm'>
        {range
          ? t('Actual coverage (Beijing time): {{start}} to {{end}}', range)
          : t('Actual coverage evidence is unavailable')}
      </p>
      {review.expenseNeedsReview ? (
        <Alert>
          <AlertTitle>{t('Expenses awaiting confirmation')}</AlertTitle>
          <AlertDescription>
            <p>
              {t(
                'Some final expenses need review. These amounts are a subtotal, not a complete bill.'
              )}
            </p>
            <p>
              {t(
                'The settlement amount also covers only confirmed consumption.'
              )}
            </p>
            {pendingTasks > 0 ? (
              <p>
                {t('Pending tasks: {{count}}', {
                  count: formatNumber(pendingTasks, locale),
                })}
              </p>
            ) : null}
            {unknownRows > 0 ? (
              <p>
                {t('Unclassified records: {{count}}', {
                  count: formatNumber(unknownRows, locale),
                })}
              </p>
            ) : null}
            {pendingSourceRows > 0 ? (
              <p>
                {t('Consumption records awaiting delivery: {{count}}', {
                  count: formatNumber(pendingSourceRows, locale),
                })}
              </p>
            ) : null}
            <ul className='mt-2 list-inside list-disc'>
              {review.expenseIssues.map((issue) => (
                <li
                  key={JSON.stringify([
                    issue.user_id,
                    issue.task_id ?? '',
                    issue.reason,
                  ])}
                >
                  {issue.user_id > 0 ? (
                    <>
                      {accounts.get(issue.user_id)?.username ?? issue.user_id}{' '}
                      ·{' '}
                    </>
                  ) : null}
                  {issue.task_id ? <>{issue.task_id} · </> : null}
                  {reasons[issue.reason] ?? t('Source evidence needs review')}
                </li>
              ))}
            </ul>
          </AlertDescription>
        </Alert>
      ) : null}
      {review.walletIssues.length > 0 ? (
        <Alert>
          <AlertTitle>{t('Wallet flows need review')}</AlertTitle>
          <AlertDescription>
            {!review.expenseNeedsReview ? (
              <p>
                {t(
                  'These wallet issues do not change the currently confirmed consumption amount.'
                )}
              </p>
            ) : null}
            <ul className='mt-2 list-inside list-disc'>
              {review.walletIssues.map((issue) => {
                const difference = accounts.get(issue.user_id)?.financials
                  .reconciliation_difference_amount_usd
                return (
                  <li
                    key={JSON.stringify([
                      issue.user_id,
                      issue.task_id ?? '',
                      issue.reason,
                    ])}
                  >
                    {issue.user_id > 0 ? (
                      <>
                        {accounts.get(issue.user_id)?.username ?? issue.user_id}{' '}
                        ·{' '}
                      </>
                    ) : null}
                    {issue.task_id ? <>{issue.task_id} · </> : null}
                    {reasons[issue.reason] ?? t('Source evidence needs review')}
                    {difference !== undefined && difference.trim() !== '' ? (
                      <>
                        {' '}
                        · {t('Recorded reconciliation difference')}:{' '}
                        {formatOrganizationInvoiceUSD(difference)}
                      </>
                    ) : null}
                  </li>
                )
              })}
            </ul>
          </AlertDescription>
        </Alert>
      ) : null}
    </div>
  )
}
