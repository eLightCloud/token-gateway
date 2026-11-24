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
  OrganizationInvoice,
  OrganizationInvoiceParams,
} from './invoice-types'
import type { ApiResponse } from './types'

type OrganizationInvoiceScope = {
  mode: 'admin' | 'current'
  organizationId?: number
  authenticatedUserId?: number
}

function organizationInvoiceBasePath(organizationId?: number): string {
  if (organizationId) {
    return `/api/admin/organizations/${organizationId}/invoice`
  }
  return '/api/organization/current/invoice'
}

function organizationInvoiceQuery(params: OrganizationInvoiceParams): string {
  return new URLSearchParams(params).toString()
}

export const organizationInvoiceKeys = {
  scope: (scope: OrganizationInvoiceScope) =>
    [
      scope.mode,
      'organization',
      scope.organizationId ?? 'unknown',
      scope.authenticatedUserId ?? 'anonymous',
      'invoice',
    ] as const,
  invoice: (
    scope: OrganizationInvoiceScope,
    params: OrganizationInvoiceParams
  ) =>
    [
      ...organizationInvoiceKeys.scope(scope),
      params.start_date,
      params.end_date,
    ] as const,
}

export async function getOrganizationInvoice(
  params: OrganizationInvoiceParams,
  organizationId?: number,
  refresh = false,
  signal?: AbortSignal
): Promise<ApiResponse<OrganizationInvoice>> {
  const query = organizationInvoiceQuery(params)
  const response = await api.get(
    `${organizationInvoiceBasePath(organizationId)}?${query}${refresh ? '&refresh=1' : ''}`,
    { signal, disableDuplicate: Boolean(signal) }
  )
  return response.data
}

export function buildOrganizationInvoiceExportUrl(
  params: OrganizationInvoiceParams,
  organizationId?: number
): string {
  return `${organizationInvoiceBasePath(organizationId)}/export?${organizationInvoiceQuery(params)}`
}
