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
import {
  hashKey,
  queryOptions,
  useMutation,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query'
import { useRef, useState } from 'react'

import { requireServerSuccess } from '@/lib/server-error-message'

import { organizationKeys } from './api'
import {
  recalculateOrganizationUsage,
  useOrganizationRecalculation,
} from './organization-recalculation'
import {
  usageSnapshotOptions,
  shouldTrackOrganizationUsage,
  type OrganizationUsageQueryScope as UsageScope,
} from './organization-usage-query-options'
import type { OrganizationUsageParams } from './types'
import { getBeijingUsageDefaultRange } from './usage-default-range'
import {
  getAdminOrganizationBillingUsageLogs,
  getCurrentOrganizationBillingUsageLogs,
} from './usage-snapshot-api'
import { dateToUnix, unixEndOfDate, useUsageFilters } from './use-usage-filters'
import { useUsageLogCursor } from './use-usage-log-cursor'

function usageLogOptions(
  scope: UsageScope,
  params: OrganizationUsageParams,
  cursor: string
) {
  return queryOptions({
    queryKey: scope.admin
      ? organizationKeys.adminUsageLogsCursor(
          scope.organizationId,
          params,
          cursor
        )
      : organizationKeys.usageLogsCursor(params, cursor, scope.organizationId),
    queryFn: async ({ signal }) => {
      const request = { ...params, page_size: 20, cursor: cursor || undefined }
      return requireServerSuccess(
        await (scope.admin
          ? getAdminOrganizationBillingUsageLogs(
              scope.organizationId,
              request,
              signal
            )
          : getCurrentOrganizationBillingUsageLogs(request, signal))
      )
    },
    staleTime: Infinity,
    retry: false,
    refetchOnMount: false,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  })
}

export function useOrganizationUsageQuery(
  props: UsageScope & {
    mode: 'billing' | 'logs'
    enabled: boolean
  }
) {
  const queryClient = useQueryClient()
  const defaultRange =
    props.mode === 'billing' ? getBeijingUsageDefaultRange() : undefined
  const defaultParams: OrganizationUsageParams = {
    start_timestamp: dateToUnix(defaultRange?.startDate ?? ''),
    end_timestamp: unixEndOfDate(defaultRange?.endDate ?? ''),
  }
  const filters = useUsageFilters(defaultRange)
  const draftParams: OrganizationUsageParams = {
    start_timestamp: filters.params.start_timestamp,
    end_timestamp: filters.params.end_timestamp,
    user_id: filters.params.user_id,
    model_name: filters.params.model_name,
    channel: props.mode === 'billing' ? filters.params.channel : undefined,
  }
  const [appliedParams, setAppliedParams] = useState<OrganizationUsageParams>(
    () => defaultParams
  )
  const scopeKey = `${props.admin ? 'admin' : 'current'}:${props.organizationId}`
  const [previousScope, setPreviousScope] = useState(scopeKey)
  if (previousScope !== scopeKey) {
    setPreviousScope(scopeKey)
    setAppliedParams(defaultParams)
    filters.setStartDate(defaultRange?.startDate ?? '')
    filters.setEndDate(defaultRange?.endDate ?? '')
    filters.setUserId('')
    filters.setModelName('')
    filters.setChannelId('')
  }
  const params = previousScope === scopeKey ? appliedParams : defaultParams
  const directoryParams: OrganizationUsageParams = {
    start_timestamp: params.start_timestamp,
    end_timestamp: params.end_timestamp,
  }
  const directorySnapshotOptions = usageSnapshotOptions(props, directoryParams)
  const sharedDirectory =
    props.mode === 'billing' && hashKey([params]) === hashKey([directoryParams])
  const directoryQuery = useQuery({
    ...directorySnapshotOptions,
    enabled: (query) =>
      props.enabled &&
      query.state.status !== 'error' &&
      (query.state.status === 'pending' ||
        shouldTrackOrganizationUsage(query.state.data?.data)),
    // The result observer owns tracking when both views share one cache key.
    // A separate timer on the directory would issue duplicate status reads.
    refetchInterval: sharedDirectory
      ? false
      : directorySnapshotOptions.refetchInterval,
  })
  const usageSnapshotQuery = useQuery({
    ...usageSnapshotOptions(props, params),
    enabled: (query) =>
      props.enabled &&
      props.mode === 'billing' &&
      query.state.status !== 'error' &&
      (query.state.status === 'pending' ||
        shouldTrackOrganizationUsage(query.state.data?.data)),
  })
  const [logsCursorStack, setLogsCursorStack] = useUsageLogCursor(
    usageLogOptions(props, params, '').queryKey
  )
  const logsQuery = useQuery({
    ...usageLogOptions(props, params, logsCursorStack.at(-1) ?? ''),
    enabled: (query) =>
      props.enabled &&
      props.mode === 'logs' &&
      query.state.status === 'pending',
  })
  const submitting = useRef(false)
  const refreshMutation = useMutation({
    mutationFn: async (submittedParams: OrganizationUsageParams) => {
      // Start the request before committing its key so useQuery observes the
      // same in-flight request, including when the conditions did not change.
      const requests: Promise<unknown>[] = []
      if (props.mode === 'billing') {
        requests.push(
          queryClient.fetchQuery({
            ...usageSnapshotOptions(props, submittedParams),
            staleTime: 0,
          })
        )
      } else {
        const firstPage = usageLogOptions(props, submittedParams, '')
        // A signed cursor freezes the range, not its rows. A fresh first page
        // can return the same cursor, so continuations from the previous
        // refresh must not survive into the new traversal.
        queryClient.removeQueries({
          queryKey: firstPage.queryKey.slice(0, -1),
          predicate: (query) => query.queryKey.at(-1) !== '',
        })
        requests.push(
          queryClient.fetchQuery({
            ...firstPage,
            staleTime: 0,
          })
        )
      }
      requests.push(
        queryClient.fetchQuery({
          ...usageSnapshotOptions(props, {
            start_timestamp: submittedParams.start_timestamp,
            end_timestamp: submittedParams.end_timestamp,
          }),
          staleTime: 0,
        })
      )
      setAppliedParams(submittedParams)
      setLogsCursorStack([])
      const results = await Promise.allSettled(requests)
      const failure = results.find((result) => result.status === 'rejected')
      if (failure?.status === 'rejected') throw failure.reason
    },
    meta: { errorToast: false },
    onSettled: () => {
      submitting.current = false
    },
  })
  const recalculation = useOrganizationRecalculation(async () => {
    const snapshot = usageSnapshotQuery.data?.data
    if (!snapshot?.snapshot_id) return
    const response = await recalculateOrganizationUsage(
      params,
      snapshot,
      props.admin ? props.organizationId : undefined
    )
    queryClient.setQueryData(
      usageSnapshotOptions(props, params).queryKey,
      response
    )
  })
  const resultQuery = props.mode === 'billing' ? usageSnapshotQuery : logsQuery
  const isRefreshing =
    refreshMutation.isPending ||
    resultQuery.isFetching ||
    recalculation.isRecalculating
  const directorySnapshot = directoryQuery.data?.data
  const payload = directorySnapshot?.payload
  const directoryError =
    directoryQuery.error ??
    (directorySnapshot?.status === 'failed'
      ? new Error(directorySnapshot.error)
      : null)

  return {
    ...recalculation,
    filters,
    appliedParams: params,
    hasChanges:
      hashKey([draftParams]) !== hashKey([params]) || !filters.isDateRangeValid,
    isRefreshing,
    directoryError,
    directoryOptions: payload
      ? {
          members: payload.members,
          models: payload.models,
          channels: payload.channels,
        }
      : undefined,
    directoryLoading: !directoryError && !payload,
    usageSnapshotQuery,
    logsQuery,
    logsParams: params,
    logsCursorStack,
    setLogsCursorStack,
    refresh: () => {
      if (
        !props.enabled ||
        !filters.isDateRangeValid ||
        submitting.current ||
        isRefreshing
      ) {
        return
      }
      submitting.current = true
      refreshMutation.mutate({ ...draftParams })
    },
    retry: () => {
      if (!props.enabled || submitting.current || isRefreshing) return
      submitting.current = true
      refreshMutation.mutate({ ...params })
    },
  }
}
