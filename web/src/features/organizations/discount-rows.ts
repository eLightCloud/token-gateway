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
import type { OrganizationChannelDiscount } from './discount-types'

export interface DiscountRow {
  key: number
  channelId: number
  ratio: string
}

export function rowsFromDiscounts(
  discounts: OrganizationChannelDiscount[] | undefined
): DiscountRow[] {
  return (discounts ?? []).map((item, index) => ({
    key: index + 1,
    channelId: item.channel_id,
    ratio: item.ratio,
  }))
}

export function rowsToPayload(rows: DiscountRow[]) {
  return rows.map((row) => ({
    channel_id: row.channelId,
    ratio: row.ratio.trim(),
  }))
}

export function rowsSignature(rows: DiscountRow[]): string {
  return [...rows]
    .map((row) => `${row.channelId}:${row.ratio.trim()}`)
    .sort()
    .join('|')
}

export function nextDiscountRowKey(rows: DiscountRow[]): number {
  return rows.reduce((max, row) => Math.max(max, row.key), 0) + 1
}

export function isValidDiscountRatio(value: string): boolean {
  const normalized = value.trim()
  if (!/^(?:0(?:\.\d{1,6})?|1(?:\.0{1,6})?)$/.test(normalized)) {
    return false
  }
  return Number(normalized) > 0
}

export function hasInvalidDiscountRows(rows: DiscountRow[]): boolean {
  if (
    rows.some((row) => row.channelId <= 0 || !isValidDiscountRatio(row.ratio))
  ) {
    return true
  }
  const channelIds = rows.map((row) => row.channelId)
  return new Set(channelIds).size !== channelIds.length
}
