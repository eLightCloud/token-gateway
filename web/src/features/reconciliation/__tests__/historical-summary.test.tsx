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

import { BillSummaryPanel } from '../components/bill-summary'

it('renders the recorded decimal net and the number of missing snapshots', () => {
  render(
    <BillSummaryPanel
      isLoading={false}
      summary={{
        perspective: 'customer',
        consume_logging_enabled: true,
        consume_quota: 2,
        refund_quota: -1,
        net_quota: 1,
        record_count: 3,
        prompt_tokens: 0,
        completion_tokens: 0,
        filter_options: { organizations: [], models: [], channels: [] },
        historical_amounts: {
          consume_cny: '1000000000.00000001',
          refund_cny: '-1000000000.00000000',
          net_cny: '0.00000001',
          recorded: 2,
          missing: 1,
        },
      }}
    />
  )

  expect(screen.getByText('¥0.00000001')).toBeVisible()
  expect(
    screen.getByText('Records without a historical exchange rate: 1')
  ).toBeVisible()
})
