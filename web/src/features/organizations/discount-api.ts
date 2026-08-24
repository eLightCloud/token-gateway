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
import { api } from '@/lib/api'

import type {
  OrganizationDiscount,
  OrganizationDiscountChannelOption,
  OrganizationDiscountHistoryPage,
  OrganizationDiscountPayload,
} from './discount-types'
import type { ApiResponse } from './types'

export const organizationDiscountKeys = {
  current: (organizationId: number) =>
    ['admin', 'organizations', organizationId, 'discount'] as const,
  history: (organizationId: number, page: number, pageSize: number) =>
    [
      'admin',
      'organizations',
      organizationId,
      'discount',
      'history',
      page,
      pageSize,
    ] as const,
}

export async function getAdminOrganizationDiscount(
  organizationId: number
): Promise<ApiResponse<OrganizationDiscount>> {
  const res = await api.get(
    `/api/admin/organizations/${organizationId}/discount`
  )
  return res.data
}

export async function updateAdminOrganizationDiscount(
  organizationId: number,
  payload: OrganizationDiscountPayload
): Promise<ApiResponse<OrganizationDiscount>> {
  const res = await api.put(
    `/api/admin/organizations/${organizationId}/discount`,
    payload,
    // 409/400 由面板就地提示（含冲突刷新引导），跳过全局错误 toast。
    { skipErrorHandler: true }
  )
  return res.data
}

export async function getAdminOrganizationDiscountHistory(
  organizationId: number,
  page: number,
  pageSize: number
): Promise<ApiResponse<OrganizationDiscountHistoryPage>> {
  const res = await api.get(
    `/api/admin/organizations/${organizationId}/discount/history?p=${page}&page_size=${pageSize}`
  )
  return res.data
}

// 渠道选项来自全局渠道表而非账单聚合：未产生消费的新渠道同样可配置折扣。
export async function getAdminOrganizationDiscountChannelOptions(
  organizationId: number
): Promise<ApiResponse<OrganizationDiscountChannelOption[]>> {
  const res = await api.get(
    `/api/admin/organizations/${organizationId}/discount/channel-options`
  )
  return res.data
}
