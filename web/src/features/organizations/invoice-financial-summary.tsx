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

import { OrganizationInvoiceFinancialNotes } from './invoice-financial-notes'
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
  return (
    <>
      <OrganizationInvoiceFinancialNotes invoice={props.invoice} />
      <div className='overflow-x-auto'>
        <Table className='min-w-max'>
          <TableHeader>
            <TableRow>
              <TableHead className='bg-background sticky left-0 z-10 min-w-48'>
                {t('Account')}
              </TableHead>
              <TableHead className='min-w-40 text-right'>
                {t('Confirmed consumption amount')}
              </TableHead>
              <TableHead className='min-w-36 text-right'>
                {t('Opening balance')}
              </TableHead>
              <TableHead className='min-w-36 text-right'>
                {t('Top-up inflow')}
              </TableHead>
              <TableHead className='min-w-36 text-right'>
                {t('Administrator balance increases')}
              </TableHead>
              <TableHead className='min-w-36 text-right'>
                {t('Other inflow (including refunds)')}
              </TableHead>
              <TableHead className='min-w-32 text-right'>
                {t('Inflow total')}
              </TableHead>
              <TableHead className='min-w-40 text-right'>
                {t('Wallet charges (before refunds)')}
              </TableHead>
              <TableHead className='min-w-36 text-right'>
                {t('Administrator balance decreases')}
              </TableHead>
              <TableHead className='min-w-36 text-right'>
                {t('Other wallet deductions')}
              </TableHead>
              <TableHead className='min-w-36 text-right'>
                {t('Deduction total')}
              </TableHead>
              <TableHead className='min-w-36 text-right'>
                {t('Balance at cutoff')}
              </TableHead>
              <TableHead className='min-w-36 text-right'>
                {t('Current balance (at query time)')}
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
                  {account.financials.wallet_refund_amount_usd &&
                  Number(account.financials.wallet_refund_amount_usd) !== 0 ? (
                    <span className='text-muted-foreground block text-xs'>
                      {t('Refund included')}:{' '}
                      {formatOrganizationInvoiceUSD(
                        account.financials.wallet_refund_amount_usd
                      )}
                    </span>
                  ) : null}
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
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
    </>
  )
}
