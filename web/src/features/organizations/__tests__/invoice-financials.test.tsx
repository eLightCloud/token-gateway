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
import { describe, test } from 'node:test'

import { createInstance } from 'i18next'
import { renderToStaticMarkup } from 'react-dom/server'
import { I18nextProvider } from 'react-i18next'

import { OrganizationInvoiceFinancialSummary } from '../invoice-financial-summary'
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
      settled_total_amount_usd: '0',
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
    assert.match(html, /Total inflow/)
  })
})
