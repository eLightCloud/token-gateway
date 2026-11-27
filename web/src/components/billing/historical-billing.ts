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
import { formatPricingAmount } from '@/features/model-pricing/currency'

export type HistoricalAmounts = {
  consume_cny: string | number
  refund_cny: string | number
  net_cny?: string | number
  recorded: number
  missing: number
}

const CNY = { label: 'CNY', symbol: '¥', exchangeRate: 1 }

export function formatHistoricalCNY(amount: string | number): string {
  if (typeof amount === 'string' && /^-?\d+(?:\.\d+)?$/.test(amount)) {
    return `¥${amount.includes('.') ? amount.replace(/0+$/, '').replace(/\.$/, '') : amount}`
  }
  return formatPricingAmount(amount, CNY)
}
