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
import assert from 'node:assert/strict'

import { act, render, screen, within } from '@testing-library/react'
import { createInstance } from 'i18next'
import { renderToStaticMarkup } from 'react-dom/server'
import { I18nextProvider } from 'react-i18next'
import { describe, test, expect } from 'vitest'

import en from '@/i18n/locales/en.json'
import zh from '@/i18n/locales/zh.json'

import { OrganizationInvoiceFinancialSummary } from '../invoice-financial-summary'
import { OrganizationInvoiceReconciliation } from '../invoice-reconciliation'
import type { OrganizationInvoice } from '../invoice-types'

describe('organization invoice financial summary', () => {
  test('renders an account with inflow even when its usage is zero', async () => {
    const i18n = createInstance()
    await i18n.init({ lng: 'en', resources: { en: { translation: {} } } })
    const invoice: OrganizationInvoice = {
      generation_status: 'ready',
      source_as_of: 2,
      calculation_version: 1,
      revision: 1,
      period: {
        start_date: '2026-08-01',
        end_date: '2026-08-31',
        timezone: 'Asia/Shanghai',
        start_timestamp: 1,
        end_timestamp: 2,
      },
      currency: 'USD',
      accounts: [
        {
          user_id: 20,
          username: 'funded-without-usage',
          gross_quota: 0,
          gross_amount_usd: '0',
          financials: {
            opening_balance_amount_usd: '0',
            payment_top_up_amount_usd: '0',
            admin_increase_amount_usd: '1500',
            other_identified_inflow_amount_usd: '0',
            admin_decrease_amount_usd: '0',
            total_inflow_amount_usd: '1500',
            ai_wallet_deduction_amount_usd: '0',
            other_deduction_amount_usd: '0',
            total_deduction_amount_usd: '0',
            closing_balance_amount_usd: '1500',
            current_balance_amount_usd: '1500',
            reconciliation_status: 'reconciled',
            reconciliation_difference_amount_usd: '0',
            calculation_version: 1,
            net_delta_quota: 750000000,
          },
        },
      ],
      category_rows: [],
      model_rows: [],
      gross_total_quota: 0,
      gross_total_amount_usd: '0',
    }

    const html = renderToStaticMarkup(
      <I18nextProvider i18n={i18n}>
        <OrganizationInvoiceFinancialSummary
          invoice={invoice}
          isLoading={false}
        />
      </I18nextProvider>
    )

    assert.match(html, /funded-without-usage/)
    assert.match(html, /1,500\.0000/)
    assert.match(html, /Inflow total/)
  })
})

test('unknown final fee keeps the confirmed subtotal visible and names the unresolved task', async () => {
  const i18n = createInstance()
  await i18n.init({ lng: 'en', resources: { en: { translation: {} } } })
  const invoice = {
    generation_status: 'ready',
    gross_total_amount_usd: '0.347618',
    expense_coverage: {
      status: 'incomplete',
      pending_tasks: 1,
      unknown_rows: 0,
      issues: [
        {
          user_id: 91,
          task_id: 'task-2820',
          reason: 'final_expense_evidence_missing',
          reserved_quota: 100,
        },
        { user_id: 0, reason: 'consume_history_cleared' },
      ],
    },
    accounts: [],
    wallet_warning: 'opening evidence unavailable',
  } as unknown as OrganizationInvoice
  const html = renderToStaticMarkup(
    <I18nextProvider i18n={i18n}>
      <OrganizationInvoiceReconciliation invoice={invoice} />
    </I18nextProvider>
  )
  expect(html).toContain('Expenses awaiting confirmation')
  expect(html).toContain('task-2820')
  expect(html).toContain('Final expense is not confirmed')
  expect(html).toContain('Historical consumption records have been cleared')
  expect(html).toContain('Wallet flows need review')
})

test('an unavailable wallet never turns a known expense into a zero bill', async () => {
  const i18n = createInstance()
  await i18n.init({ lng: 'en', resources: { en: { translation: {} } } })
  const invoice = {
    expense_coverage: {
      status: 'complete',
      pending_tasks: 0,
      unknown_rows: 0,
      wallet_issues: [
        {
          user_id: 91,
          task_id: 'pending-refund',
          reason: 'task_funding_pending',
        },
      ],
    },
    accounts: [],
    wallet_warning: 'historical wallet evidence is missing',
  } as unknown as OrganizationInvoice
  const html = renderToStaticMarkup(
    <I18nextProvider i18n={i18n}>
      <OrganizationInvoiceReconciliation invoice={invoice} />
    </I18nextProvider>
  )
  expect(html).toContain('Wallet flows need review')
  expect(html).toContain('Task funding is pending')
  expect(html).not.toContain('Expenses awaiting confirmation')
})

function completeInvoice(): OrganizationInvoice {
  return {
    generation_status: 'ready',
    source_as_of: 1790783999,
    calculation_version: 10,
    revision: 2,
    currency: 'USD',
    period: {
      start_date: '2026-09-01',
      end_date: '2026-09-30',
      timezone: 'Asia/Shanghai',
      start_timestamp: 1788192000,
      end_timestamp: 1790783999,
    },
    coverage_period: {
      start_date: '2026-09-01',
      end_date: '2026-09-30',
      timezone: 'Asia/Shanghai',
      start_timestamp: 1788192000,
      end_timestamp: 1790783999,
    },
    expense_coverage: {
      status: 'complete',
      total_rows: 1,
      total_quota: 100,
      ordinary_rows: 1,
      ordinary_quota: 100,
      task_rows: 0,
      task_quota: 0,
      unknown_rows: 0,
      unknown_quota: 0,
      pending_tasks: 0,
      missing_task_logs: 0,
    },
    accounts: [
      {
        user_id: 91,
        username: 'alice',
        gross_quota: 100,
        gross_amount_usd: '20',
        financials: {
          opening_balance_amount_usd: '0',
          payment_top_up_amount_usd: '100',
          admin_increase_amount_usd: '0',
          other_identified_inflow_amount_usd: '80',
          total_inflow_amount_usd: '180',
          ai_wallet_deduction_amount_usd: '100',
          admin_decrease_amount_usd: '0',
          other_deduction_amount_usd: '0',
          total_deduction_amount_usd: '100',
          closing_balance_amount_usd: '80',
          current_balance_amount_usd: '70',
          wallet_refund_amount_usd: '80',
          reconciliation_status: 'derived',
          calculation_version: 10,
          net_delta_quota: 0,
        },
      },
    ],
    category_rows: [],
    model_rows: [],
    gross_total_quota: 100,
    gross_total_amount_usd: '20',
  }
}

async function financialMarkup(invoice: OrganizationInvoice, summary = false) {
  const i18n = createInstance()
  await i18n.init({ lng: 'en', resources: { en: { translation: {} } } })
  return renderToStaticMarkup(
    <I18nextProvider i18n={i18n}>
      {summary ? (
        <OrganizationInvoiceFinancialSummary
          invoice={invoice}
          isLoading={false}
        />
      ) : (
        <OrganizationInvoiceReconciliation invoice={invoice} />
      )}
    </I18nextProvider>
  )
}

test('normal derived balances and ordinary refunds do not produce diagnostic alerts', async () => {
  const html = await financialMarkup(completeInvoice())
  expect(html).not.toContain('role="alert"')
  expect(html).not.toContain('independently verified')
  expect(html).not.toContain('Pending tasks')
})

test.each(['missing', 'empty', 'incomplete', 'counter', 'issue'] as const)(
  'coverage %s cannot silently become a complete bill',
  async (kind) => {
    const invoice = completeInvoice()
    assert.ok(invoice.expense_coverage)
    assert.ok(invoice.coverage_period)
    if (kind === 'missing') delete invoice.expense_coverage
    else if (kind === 'empty') {
      invoice.expense_coverage.status = '' as 'incomplete'
    } else if (kind === 'incomplete') {
      invoice.expense_coverage.status = 'incomplete'
    } else if (kind === 'counter') {
      invoice.expense_coverage.pending_source_rows = 1
    } else {
      invoice.expense_coverage.issues = [
        { user_id: 0, reason: 'consume_history_cleared', reserved_quota: 0 },
      ]
    }
    const html = await financialMarkup(invoice)
    expect(html).toContain('Expenses awaiting confirmation')
    expect(html).toContain('subtotal, not a complete bill')
    expect(html).not.toContain('The settlement amount')
  }
)

test('wallet-only account gaps name the affected account without exposing raw diagnostics', async () => {
  const invoice = completeInvoice()
  assert.ok(invoice.expense_coverage)
  assert.ok(invoice.coverage_period)
  invoice.accounts[0].financials.reconciliation_status = 'unavailable'
  invoice.wallet_warning =
    'SELECT secret FROM private_table: password=do-not-display'
  const html = await financialMarkup(invoice)
  expect(html).toContain('Wallet flows need review')
  expect(html).toContain('alice')
  expect(html).not.toContain('Expenses awaiting confirmation')
  expect(html).not.toContain('private_table')
  expect(html).not.toContain('do-not-display')
})

test('duplicate task problems are listed once and organization problems do not invent an account', async () => {
  const invoice = completeInvoice()
  assert.ok(invoice.expense_coverage)
  assert.ok(invoice.coverage_period)
  const issue = {
    user_id: 91,
    task_id: 'task-one',
    reason: 'final_expense_evidence_missing',
    reserved_quota: 100,
  }
  invoice.expense_coverage.issues = [
    issue,
    issue,
    { user_id: 0, reason: 'consume_history_cleared', reserved_quota: 0 },
  ]
  const html = await financialMarkup(invoice)
  expect(html.match(/task-one/g)).toHaveLength(1)
  expect(html).toContain('alice')
  expect(html).not.toContain('0 ·')
})

test('the financial table distinguishes confirmed expenses from gross wallet charges and hides normal diagnostics', async () => {
  const invoice = completeInvoice()
  assert.ok(invoice.expense_coverage)
  assert.ok(invoice.coverage_period)
  const html = await financialMarkup(invoice, true)
  expect(html).toContain('Confirmed consumption amount')
  expect(html).toContain('Wallet charges (before refunds)')
  expect(html).toContain('Current balance (at query time)')
  expect(html).toContain('100.0000')
  expect(html).toContain('20.0000')
  expect(html).toContain('Refund included')
  expect(html).not.toContain('Reconciliation</th>')
  expect(html).not.toContain('Difference</th>')
  expect(html).not.toContain('Derived')
  expect(html).toContain('2026-09-30')
  invoice.accounts[0].financials.wallet_refund_amount_usd = '0.0000000000'
  expect(await financialMarkup(invoice, true)).not.toContain('Refund included')
})

test('an intraday frozen cutoff shows Beijing time instead of the requested month end', async () => {
  const invoice = completeInvoice()
  assert.ok(invoice.expense_coverage)
  assert.ok(invoice.coverage_period)
  invoice.coverage_period.end_timestamp = 1789444800 // 2026-09-15 12:00 Beijing
  invoice.coverage_period.end_date = '2026-09-15'
  const html = await financialMarkup(invoice)
  expect(html).toContain('2026-09-15 12:00:00')
  expect(html).not.toContain('2026-09-30')
})

test('financial headers and explanations follow the selected language without changing snapshot amounts', async () => {
  const invoice = completeInvoice()
  assert.ok(invoice.expense_coverage)
  assert.ok(invoice.coverage_period)
  const snapshot = JSON.stringify(invoice)
  const i18n = createInstance()
  await i18n.init({ lng: 'en', resources: { en, zh } })
  render(
    <I18nextProvider i18n={i18n}>
      <OrganizationInvoiceFinancialSummary
        invoice={invoice}
        isLoading={false}
      />
    </I18nextProvider>
  )
  expect(
    screen.getByRole('columnheader', {
      name: 'Wallet charges (before refunds)',
    })
  ).toBeVisible()
  expect(screen.getAllByRole('columnheader')).toHaveLength(13)
  expect(screen.getByText(/final charge recorded for usage/)).toBeVisible()
  const row = screen.getByText('alice').closest('tr')
  assert.ok(row)
  expect(within(row).getAllByRole('cell')).toHaveLength(13)
  expect(within(row).getAllByText('100.0000').length).toBeGreaterThan(0)
  await act(async () => {
    await i18n.changeLanguage('zh')
  })
  expect(
    screen.getByRole('columnheader', { name: '钱包扣款（退款前）' })
  ).toBeVisible()
  expect(
    screen.getByRole('columnheader', { name: '当前余额（查询时）' })
  ).toBeVisible()
  expect(screen.getByText(/用量对应的已入账最终费用/)).toBeVisible()
  expect(JSON.stringify(invoice)).toBe(snapshot)
})

test('a wallet problem with a recorded difference shows only the known account and difference', async () => {
  const invoice = completeInvoice()
  assert.ok(invoice.expense_coverage)
  assert.ok(invoice.coverage_period)
  invoice.accounts[0].financials.reconciliation_status = 'incomplete'
  invoice.accounts[0].financials.reconciliation_difference_amount_usd = '1.25'
  const html = await financialMarkup(invoice)
  expect(html).toContain('Wallet flow evidence is incomplete')
  expect(html).toContain('Recorded reconciliation difference')
  expect(html).toContain('1.2500')
  expect(html).not.toContain('Expenses awaiting confirmation')
})
