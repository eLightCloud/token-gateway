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
export interface OrganizationChannelDiscount {
  channel_id: number
  ratio: string
}

export interface OrganizationDiscount {
  snapshot_id: number
  channel_discounts: OrganizationChannelDiscount[]
  created_by?: number
  created_at?: number
}

export interface OrganizationDiscountPayload {
  expected_snapshot_id: number
  channel_discounts: Array<{ channel_id: number; ratio: string }>
}

export interface OrganizationDiscountChange {
  channel_id: number
  /** 空字符串表示该侧未配置 */
  old_ratio: string
  new_ratio: string
}

export interface OrganizationDiscountHistoryItem {
  snapshot_id: number
  channel_discounts: OrganizationChannelDiscount[]
  changes: OrganizationDiscountChange[]
  created_by: number
  created_by_name: string
  created_at: number
}

export interface OrganizationDiscountHistoryPage {
  page: number
  page_size: number
  total: number
  items: OrganizationDiscountHistoryItem[]
}

export interface OrganizationDiscountChannelOption {
  id: number
  name: string
  status: number
}
