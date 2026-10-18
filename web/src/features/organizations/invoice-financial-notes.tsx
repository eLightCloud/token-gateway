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
import { useTranslation } from 'react-i18next'

import { formatOrganizationInvoiceCoverage } from './invoice-presentation'
import type { OrganizationInvoice } from './invoice-types'

export function OrganizationInvoiceFinancialNotes(props: {
  invoice: OrganizationInvoice
}) {
  const { t } = useTranslation()
  const coverage = formatOrganizationInvoiceCoverage(
    props.invoice.coverage_period
  )
  return (
    <div className='text-muted-foreground space-y-1 px-4 pb-3 text-sm'>
      <p>
        {t(
          'Wallet charges cover AI usage, including recorded task precharges and extra charges, before refunds. Deduction totals also include administrator and other deductions.'
        )}{' '}
        {t(
          'Refunds are included in other inflow, not added again. Unresolved expenses are described in the review notice.'
        )}
      </p>
      <p>
        {t(
          'Confirmed consumption means the final fee is determined, before organization settlement factors. It does not mean the administrator has approved or paid the invoice.'
        )}
      </p>
      <p>
        {coverage ? (
          <>
            {t(
              'Closing balance is derived from opening balance and period flows through {{cutoff}} (Beijing time).',
              { cutoff: coverage.end }
            )}{' '}
          </>
        ) : null}
        {t(
          'Current balance is read at query time. This page does not continuously refresh wallet balances.'
        )}
      </p>
    </div>
  )
}
