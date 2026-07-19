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
  OrganizationSettlementRuleOption,
  OrganizationSettlementRuleUpdatePayload,
  OrganizationSettlementRuleUpdateResult,
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
  rules: (scope: OrganizationInvoiceScope, effectiveMonth: string) =>
    [
      ...organizationInvoiceKeys.scope(scope),
      'settlement-rules',
      effectiveMonth,
    ] as const,
}

export async function getOrganizationInvoice(
  params: OrganizationInvoiceParams,
  organizationId?: number
): Promise<ApiResponse<OrganizationInvoice>> {
  const response = await api.get(
    `${organizationInvoiceBasePath(organizationId)}?${organizationInvoiceQuery(params)}`
  )
  return response.data
}

export async function getOrganizationSettlementRules(
  effectiveMonth: string,
  organizationId?: number
): Promise<ApiResponse<OrganizationSettlementRuleOption[]>> {
  const query = new URLSearchParams({
    effective_month: effectiveMonth,
  }).toString()
  const response = await api.get(
    `${organizationInvoiceBasePath(organizationId)}/settlement-rules?${query}`
  )
  return response.data
}

export async function updateOrganizationSettlementRule(
  payload: OrganizationSettlementRuleUpdatePayload,
  organizationId?: number
): Promise<ApiResponse<OrganizationSettlementRuleUpdateResult>> {
  const response = await api.put(
    `${organizationInvoiceBasePath(organizationId)}/settlement-rules`,
    payload
  )
  return response.data
}

export function buildOrganizationInvoiceExportUrl(
  params: OrganizationInvoiceParams,
  organizationId?: number
): string {
  return `${organizationInvoiceBasePath(organizationId)}/export?${organizationInvoiceQuery(params)}`
}
