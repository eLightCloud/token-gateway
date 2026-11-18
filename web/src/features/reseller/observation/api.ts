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

import { useQuery } from '@tanstack/react-query'

import { useAuthStore } from '@/stores/auth-store'

import { resellerKey, resellerRequest } from '../api'
import type { ObservationSearch, ObservationView } from './routes'

export type Customer = { user_id: number; username: string }

type Overview = {
  customers: number
  awaiting: number
  active: number
  requests: number
  failures: number
}

type ActivityPoint = {
  day_start: number
  model: string
  requests: number
  tokens: number
}

export type UsageRow = Customer & {
  created_at: number
  model_name: string
  type: number
  prompt_tokens: number
  completion_tokens: number
  use_time: number
  request_id: string
}

export type AuditRow = Customer & {
  created_at: number
  event_id: string
  operator: string
  action: string
  enabled?: boolean
  quota?: number
  mode?: string
  revision?: number
}

export type TaskRow = Customer & {
  task_id: string
  action: string
  status: string
  progress: string
  submit_time: number
  start_time: number
  finish_time: number
}

type ObservationParams = ObservationSearch & {
  page?: number
  after_time?: number
  after_event?: string
}

export type ObservationRows<T> = { items: T[]; has_more: boolean }

type ObservationData = {
  customers: Customer[]
  overview: Overview
  analytics: ActivityPoint[]
  usage: ObservationRows<UsageRow>
  audit: ObservationRows<AuditRow>
  tasks: ObservationRows<TaskRow>
}

export function useObservationQuery<V extends ObservationView | 'customers'>(
  view: V,
  params: ObservationParams = {}
) {
  const userID = useAuthStore((state) => state.auth.user?.id)
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== '') search.set(key, String(value))
  }
  return useQuery({
    enabled: !!userID,
    queryKey: resellerKey(userID, 'observation', view, search.toString()),
    queryFn: () =>
      resellerRequest<ObservationData[V]>(
        `/api/reseller/self/observability/${view}?${search}`
      ),
    meta: { errorToast: false },
  })
}
