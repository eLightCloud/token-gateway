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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useRef, useState } from 'react'

import { getOrganizationInvoice, organizationInvoiceKeys } from './invoice-api'
import type { OrganizationInvoiceParams } from './invoice-types'
import {
  recalculateOrganizationInvoice,
  useOrganizationRecalculation,
} from './organization-recalculation'
import type { useUsageDateRange } from './use-usage-date-range'

export function useOrganizationInvoiceQuery(props: {
  enabled?: boolean
  organizationId?: number
  currentOrganizationId?: number
  authenticatedUserId?: number
  initialParams: OrganizationInvoiceParams
  dateRange: ReturnType<typeof useUsageDateRange>
}) {
  const client = useQueryClient()
  const [params, setParams] = useState(props.initialParams)
  const scope = {
    mode: props.organizationId ? ('admin' as const) : ('current' as const),
    organizationId: props.organizationId ?? props.currentOrganizationId,
    authenticatedUserId: props.authenticatedUserId,
  }
  const queryOptions = {
    queryKey: organizationInvoiceKeys.invoice(scope, params),
    queryFn: ({ signal }: { signal: AbortSignal }) =>
      getOrganizationInvoice(params, props.organizationId, false, signal),
    staleTime: Infinity,
    retry: false,
    refetchOnMount: false,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  }
  const invoiceQuery = useQuery({
    ...queryOptions,
    enabled: (query) =>
      props.enabled !== false &&
      Boolean(props.authenticatedUserId) &&
      query.state.status === 'pending',
    refetchInterval: false,
  })
  const submitting = useRef(false)
  const refreshMutation = useMutation({
    mutationFn: async (submitted: OrganizationInvoiceParams) => {
      const queryKey = organizationInvoiceKeys.invoice(scope, submitted)
      const request = client.fetchQuery({
        ...queryOptions,
        queryKey,
        queryFn: ({ signal }) =>
          getOrganizationInvoice(
            submitted,
            props.organizationId,
            false,
            signal
          ),
        staleTime: 0,
      })
      setParams(submitted)
      await request
    },
    meta: { errorToast: false },
    onSettled: () => {
      submitting.current = false
    },
  })
  const recalculation = useOrganizationRecalculation(async () => {
    const invoice = invoiceQuery.data?.data
    if (!invoice?.recalculation_token) return
    const response = await recalculateOrganizationInvoice(
      params,
      invoice.recalculation_token,
      props.organizationId
    )
    client.setQueryData(
      organizationInvoiceKeys.invoice(scope, params),
      response
    )
  })
  const isRefreshing =
    refreshMutation.isPending ||
    invoiceQuery.isFetching ||
    recalculation.isRecalculating
  return {
    ...recalculation,
    invoiceQuery,
    params,
    isRefreshing,
    hasChanges:
      props.dateRange.startDate !== params.start_date ||
      props.dateRange.endDate !== params.end_date,
    refresh: () => {
      if (
        props.enabled === false ||
        !props.authenticatedUserId ||
        !props.dateRange.isDateRangeValid ||
        submitting.current ||
        isRefreshing
      ) {
        return
      }
      submitting.current = true
      refreshMutation.mutate({
        start_date: props.dateRange.startDate,
        end_date: props.dateRange.endDate,
      })
    },
    retry: () => {
      if (
        props.enabled === false ||
        !props.authenticatedUserId ||
        submitting.current ||
        isRefreshing
      ) {
        return
      }
      submitting.current = true
      refreshMutation.mutate({ ...params })
    },
  }
}
