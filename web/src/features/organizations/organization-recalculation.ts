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
import { useMutation } from '@tanstack/react-query'
import { useRef } from 'react'

import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'

import { buildQuery } from './api'
import type {
  OrganizationInvoice,
  OrganizationInvoiceParams,
} from './invoice-types'
import type { ApiResponse, OrganizationUsageParams } from './types'
import type { OrganizationUsageSnapshot } from './usage-snapshot-api'

export async function recalculateOrganizationInvoice(
  params: OrganizationInvoiceParams,
  token: string,
  organizationId?: number
): Promise<ApiResponse<OrganizationInvoice>> {
  const base = organizationId
    ? `/api/admin/organizations/${organizationId}`
    : '/api/organization/current'
  const response = await api.post(
    `${base}/invoice/recalculate?${buildQuery(params)}`,
    {
      expected_token: token,
    }
  )
  return requireServerSuccess(response.data)
}

export async function recalculateOrganizationUsage(
  params: OrganizationUsageParams,
  snapshot: OrganizationUsageSnapshot,
  organizationId?: number
): Promise<ApiResponse<OrganizationUsageSnapshot>> {
  const base = organizationId
    ? `/api/admin/organizations/${organizationId}`
    : '/api/organization/current'
  // Pin an open-ended query to the exact target the confirmation refers to.
  const query = buildQuery({ ...params, end_timestamp: snapshot.range_end })
  const response = await api.post(
    `${base}/billing/usage/recalculate?${query}`,
    {
      expected_snapshot_id: snapshot.snapshot_id,
      expected_revision: snapshot.revision,
    }
  )
  return requireServerSuccess(response.data)
}

export function useOrganizationRecalculation(action: () => Promise<unknown>) {
  const submitting = useRef(false)
  const mutation = useMutation({
    mutationFn: action,
    onSettled: () => {
      submitting.current = false
    },
  })
  return {
    isRecalculating: mutation.isPending,
    recalculate: async () => {
      if (submitting.current) return
      submitting.current = true
      await mutation.mutateAsync()
    },
  }
}
