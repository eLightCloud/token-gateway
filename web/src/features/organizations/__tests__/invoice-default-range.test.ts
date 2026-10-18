/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it under the terms of
the GNU Affero General Public License as published by the Free Software
Foundation, either version 3 of the License, or (at your option) any later
version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { describe, expect, it } from 'vitest'

import { getBeijingInvoiceDefaultRange } from '../beijing-time'

// Beijing is UTC+8: a fixed UTC instant keeps every case deterministic
// regardless of the machine running the test.
const utc = (iso: string) => Date.parse(iso)

describe('getBeijingInvoiceDefaultRange', () => {
  it('covers month start through yesterday mid-month', () => {
    const range = getBeijingInvoiceDefaultRange(
      utc('2026-10-16T15:30:00Z') // Beijing 2026-10-16 23:30
    )
    expect(range).toEqual({
      startDate: '2026-10-01',
      endDate: '2026-10-15',
      effectiveMonth: '2026-10',
      currentMonthPending: false,
    })
  })

  it('covers exactly one day on the second day of a month', () => {
    const range = getBeijingInvoiceDefaultRange(
      utc('2026-10-02T01:00:00Z') // Beijing 2026-10-02 09:00
    )
    expect(range.startDate).toBe('2026-10-01')
    expect(range.endDate).toBe('2026-10-01')
    expect(range.currentMonthPending).toBe(false)
  })

  it('falls back to the previous full month on the first day', () => {
    const range = getBeijingInvoiceDefaultRange(
      utc('2026-10-01T00:30:00Z') // Beijing 2026-10-01 08:30
    )
    expect(range).toEqual({
      startDate: '2026-09-01',
      endDate: '2026-09-30',
      effectiveMonth: '2026-09',
      currentMonthPending: true,
    })
  })

  it('falls back across the year boundary on January 1st', () => {
    const range = getBeijingInvoiceDefaultRange(
      utc('2026-01-01T10:00:00Z') // Beijing 2026-01-01 18:00
    )
    expect(range.startDate).toBe('2025-12-01')
    expect(range.endDate).toBe('2025-12-31')
    expect(range.currentMonthPending).toBe(true)
  })

  it('rolls the covered day at Beijing midnight, not UTC midnight', () => {
    // UTC 2026-10-15 16:00 is already Beijing 2026-10-16, so the default
    // cutoff must advance to October 15.
    const range = getBeijingInvoiceDefaultRange(utc('2026-10-15T16:00:00Z'))
    expect(range.endDate).toBe('2026-10-15')
    expect(range.startDate).toBe('2026-10-01')
  })
})
