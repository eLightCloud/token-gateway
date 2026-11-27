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
import { render, screen } from '@testing-library/react'
import { expect, it } from 'vitest'

import { HistoricalBillAmount } from '../historical-bill-amount'

it('shows recorded USD and signed CNY amounts with the historical rate', () => {
  render(
    <HistoricalBillAmount
      quota={-50_000}
      snapshot={{
        exchange_rate: '7.3',
        quota_per_unit: '500000',
        amount: '-0.73',
      }}
    />
  )

  expect(screen.getByText('$-0.1')).toBeVisible()
  expect(screen.getByText('¥-0.73 · 1 USD = 7.3 CNY')).toBeVisible()
})

it('shows the raw quota when there is no historical exchange rate', () => {
  render(<HistoricalBillAmount quota={50} />)

  expect(screen.getByText('50 quota')).toBeVisible()
  expect(screen.getByText('Historical exchange rate unavailable')).toBeVisible()
})
