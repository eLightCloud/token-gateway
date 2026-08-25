import { useTranslation } from 'react-i18next'

import { Skeleton } from '@/components/ui/skeleton'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

import { formatOrganizationInvoiceUSD } from './invoice-format'
import type { OrganizationInvoice } from './invoice-types'

type OrganizationInvoiceFinancialSummaryProps = {
  invoice?: OrganizationInvoice
  isLoading: boolean
}

export function OrganizationInvoiceFinancialSummary(
  props: OrganizationInvoiceFinancialSummaryProps
) {
  const { t } = useTranslation()
  if (props.isLoading) {
    return (
      <div className='space-y-2 p-4'>
        <Skeleton className='h-10 w-full' />
        <Skeleton className='h-12 w-full' />
        <Skeleton className='h-12 w-full' />
      </div>
    )
  }
  if (!props.invoice?.accounts.length) return null
  const reconciliationLabels = {
    generating: t('Generating...'),
    derived: t('Derived'),
    reconciled: t('Reconciled'),
    incomplete: t('Incomplete'),
  }

  return (
    <div className='overflow-x-auto'>
      <Table className='min-w-max'>
        <TableHeader>
          <TableRow>
            <TableHead className='bg-background sticky left-0 z-10 min-w-48'>
              {t('Account')}
            </TableHead>
            <TableHead className='min-w-28 text-right'>{t('Usage')}</TableHead>
            <TableHead className='min-w-36 text-right'>
              {t('Opening balance')}
            </TableHead>
            <TableHead className='min-w-36 text-right'>
              {t('Payment top-ups')}
            </TableHead>
            <TableHead className='min-w-36 text-right'>
              {t('Admin increases')}
            </TableHead>
            <TableHead className='min-w-36 text-right'>
              {t('Other inflow')}
            </TableHead>
            <TableHead className='min-w-32 text-right'>
              {t('Total inflow')}
            </TableHead>
            <TableHead className='min-w-40 text-right'>
              {t('AI wallet deductions')}
            </TableHead>
            <TableHead className='min-w-36 text-right'>
              {t('Admin decreases')}
            </TableHead>
            <TableHead className='min-w-36 text-right'>
              {t('Other deductions')}
            </TableHead>
            <TableHead className='min-w-36 text-right'>
              {t('Total deductions')}
            </TableHead>
            <TableHead className='min-w-36 text-right'>
              {t('Closing balance')}
            </TableHead>
            <TableHead className='min-w-36 text-right'>
              {t('Current balance')}
            </TableHead>
            <TableHead className='min-w-32'>{t('Reconciliation')}</TableHead>
            <TableHead className='min-w-32 text-right'>
              {t('Difference')}
            </TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {props.invoice.accounts.map((account) => (
            <TableRow key={account.user_id}>
              <TableCell className='bg-background sticky left-0 z-10 font-medium'>
                <span className='block'>{account.username}</span>
                {account.display_name ? (
                  <span className='text-muted-foreground block font-normal'>
                    {account.display_name}
                  </span>
                ) : null}
              </TableCell>
              <TableCell className='text-right tabular-nums'>
                {formatOrganizationInvoiceUSD(account.gross_amount_usd, true)}
              </TableCell>
              <TableCell className='text-right tabular-nums'>
                {formatOrganizationInvoiceUSD(
                  account.financials.opening_balance_amount_usd,
                  true
                )}
              </TableCell>
              <TableCell className='text-right tabular-nums'>
                {formatOrganizationInvoiceUSD(
                  account.financials.payment_top_up_amount_usd,
                  true
                )}
              </TableCell>
              <TableCell className='text-right tabular-nums'>
                {formatOrganizationInvoiceUSD(
                  account.financials.admin_increase_amount_usd,
                  true
                )}
              </TableCell>
              <TableCell className='text-right tabular-nums'>
                {formatOrganizationInvoiceUSD(
                  account.financials.other_identified_inflow_amount_usd,
                  true
                )}
              </TableCell>
              <TableCell className='text-right font-medium tabular-nums'>
                {formatOrganizationInvoiceUSD(
                  account.financials.total_inflow_amount_usd
                )}
              </TableCell>
              <TableCell className='text-right tabular-nums'>
                {formatOrganizationInvoiceUSD(
                  account.financials.ai_wallet_deduction_amount_usd,
                  true
                )}
              </TableCell>
              <TableCell className='text-right tabular-nums'>
                {formatOrganizationInvoiceUSD(
                  account.financials.admin_decrease_amount_usd,
                  true
                )}
              </TableCell>
              <TableCell className='text-right tabular-nums'>
                {formatOrganizationInvoiceUSD(
                  account.financials.other_deduction_amount_usd,
                  true
                )}
              </TableCell>
              <TableCell className='text-right font-medium tabular-nums'>
                {formatOrganizationInvoiceUSD(
                  account.financials.total_deduction_amount_usd
                )}
              </TableCell>
              <TableCell className='text-right tabular-nums'>
                {formatOrganizationInvoiceUSD(
                  account.financials.closing_balance_amount_usd,
                  true
                )}
              </TableCell>
              <TableCell className='text-right tabular-nums'>
                {formatOrganizationInvoiceUSD(
                  account.financials.current_balance_amount_usd
                )}
              </TableCell>
              <TableCell>
                {reconciliationLabels[account.financials.reconciliation_status]}
              </TableCell>
              <TableCell className='text-right tabular-nums'>
                {account.financials.reconciliation_difference_amount_usd ===
                undefined
                  ? '—'
                  : formatOrganizationInvoiceUSD(
                      account.financials.reconciliation_difference_amount_usd,
                      true
                    )}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  )
}
