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
import type {
  GetUsersParams,
  GetUsersResponse,
  SearchUsersParams,
} from '@/features/users/types'
import { api } from '@/lib/api'
import { createServerError } from '@/lib/server-error-message'

export type ResellerAccount = {
  user_id: number
  balance: number
  reserved: number
  revision: number
  published_at: number
  enabled: boolean
}
export type ResellerUser = {
  user_id: number
  username: string
  display_name: string
  email: string
  activation: string
  agent_disabled: boolean
  platform_disabled: boolean
  quota: number
  account?: ResellerAccount
}
export type ConfigItem = {
  model: string
  channel_id: number
  authorized: boolean
  ratio: string
}
export type Config = { revision: number; items: ConfigItem[] }
export type Workspace = {
  pricing_phase?: string
  user: ResellerUser
  config: Config
  candidates: ConfigItem[]
  history: { revision: number; operator_id: number; created_at: number }[]
}
export type UserPage = {
  items: ResellerUser[]
  next_cursor: number
  account?: ResellerAccount
}
export type BillingRow = {
  id: number
  request_id: string
  original_operation_id: string
  usage: Record<string, number> | null
  customer_ratio: string
  agent_ratio: string
  customer_id: number
  model: string
  channel_id: number
  kind: 'reserve' | 'settle' | 'refund'
  customer_delta: number
  customer_target: number | null
  agent_target: number | null
  margin: number | null
  reserve_delta: number
  spend_delta: number
  occurred_at: number
  processed_at: number
}
export type BillingPage = {
  projection_revision: number
  items: BillingRow[]
  next_cursor: number
  until: number
  account: ResellerAccount
}
export type Adjustment = {
  id: number
  operation_id: string
  mode: string
  quota: number
  reason: string
  before: number
  after: number
  occurred_at: number
}
export type RatioRange = { model: string; min: string; max: string }

export const resellerPath = (admin: boolean) =>
  admin ? '/api/reseller/admin/agents' : '/api/reseller/self/customers'
export const resellerKey = (userID: number | undefined, ...parts: unknown[]) =>
  ['reseller', userID, ...parts] as const

export async function resellerRequest<T>(
  url: string,
  method: 'get' | 'post' | 'put' = 'get',
  body?: unknown
): Promise<T> {
  const response =
    method === 'get' ? await api.get(url) : await api[method](url, body)
  const result = response.data as {
    success: boolean
    message?: string
    data: T
  }
  if (!result.success) throw createServerError(result, 'Request failed')
  return result.data
}

export async function queryResellerAdminUsers(
  params: GetUsersParams &
    SearchUsersParams & { reseller_kind: string; agent_id: string }
): Promise<GetUsersResponse> {
  return (await api.get('/api/user/search', { params }))
    .data as GetUsersResponse
}

export type AdjustmentPage = { items: Adjustment[]; next_cursor: number }
