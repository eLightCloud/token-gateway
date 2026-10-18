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
import { queryOptions } from '@tanstack/react-query'

import { requireServerSuccess } from '@/lib/server-error-message'

import { organizationKeys } from './api'
import type { ApiResponse, OrganizationUsageParams } from './types'
import {
  getAdminOrganizationBillingUsageSnapshot,
  getCurrentOrganizationBillingUsageSnapshot,
  type OrganizationUsageSnapshot,
} from './usage-snapshot-api'

export type OrganizationUsageQueryScope =
  | { organizationId: number; admin: true }
  | { organizationId?: number; admin?: false }

export function shouldTrackOrganizationUsage(
  snapshot?: OrganizationUsageSnapshot
): boolean {
  return Boolean(
    snapshot &&
    (snapshot.status === 'building' || snapshot.status === 'queued') &&
    Number.isFinite(snapshot.range_start) &&
    Number.isFinite(snapshot.range_end) &&
    snapshot.range_end > snapshot.range_start
  )
}

export function usageSnapshotOptions(
  scope: OrganizationUsageQueryScope,
  params: OrganizationUsageParams
) {
  return queryOptions({
    queryKey: scope.admin
      ? organizationKeys.adminUsageSnapshot(scope.organizationId, params)
      : organizationKeys.usageSnapshot(params, scope.organizationId),
    queryFn: async ({ signal, client, queryKey }) => {
      const previous =
        client.getQueryData<ApiResponse<OrganizationUsageSnapshot>>(
          queryKey
        )?.data
      // Follow the submitted target, not the advancing current-day bucket.
      // Once a snapshot exists, its id selects the read-only status path.
      // Capacity-queued targets without an id retry admission for the same
      // frozen range. No tracking request can force recalculation.
      const request =
        shouldTrackOrganizationUsage(previous) && previous
          ? {
              ...params,
              start_timestamp: previous.range_start,
              end_timestamp: previous.range_end,
            }
          : params
      const snapshotId =
        shouldTrackOrganizationUsage(previous) &&
        previous &&
        previous.snapshot_id > 0
          ? previous.snapshot_id
          : undefined
      return requireServerSuccess(
        await (scope.admin
          ? getAdminOrganizationBillingUsageSnapshot(
              scope.organizationId,
              request,
              false,
              signal,
              snapshotId
            )
          : getCurrentOrganizationBillingUsageSnapshot(
              request,
              false,
              signal,
              snapshotId
            ))
      )
    },
    refetchInterval: (query) => {
      if (
        query.state.status === 'error' ||
        !shouldTrackOrganizationUsage(query.state.data?.data)
      ) {
        return false
      }
      return Math.min(
        2_000 + Math.max(0, query.state.dataUpdateCount - 1) * 500,
        10_000
      )
    },
    refetchIntervalInBackground: false,
    staleTime: Infinity,
    retry: false,
    refetchOnMount: false,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  })
}
