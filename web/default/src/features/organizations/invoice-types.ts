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
export type OrganizationInvoiceParams = {
  start_date: string
  end_date: string
}

export type OrganizationInvoicePeriod = OrganizationInvoiceParams & {
  timezone: string
  start_timestamp: number
  end_timestamp: number
}

export type OrganizationInvoiceAccount = {
  user_id: number
  username: string
  display_name?: string
  gross_quota: number
  gross_amount_usd: string
}

export type OrganizationInvoiceAccountAmount = {
  user_id: number
  gross_quota: number
  gross_amount_usd: string
}

export type OrganizationInvoiceFactorSegment = {
  period_month: string
  factor: string
  factor_scaled: number
  rule_effective_month?: string
  rule_version: number
  gross_quota: number
  settled_amount_usd: string
}

export type OrganizationInvoiceCategoryRow = {
  category_key: string
  category_name: string
  fallback: boolean
  models: string[]
  account_amounts: OrganizationInvoiceAccountAmount[]
  gross_quota: number
  gross_amount_usd: string
  factor: string
  multiple_factors: boolean
  factor_segments: OrganizationInvoiceFactorSegment[]
  settled_amount_usd: string
}

export type OrganizationInvoiceModelRow = {
  model_name: string
  category_key: string
  account_amounts: OrganizationInvoiceAccountAmount[]
  gross_quota: number
  gross_amount_usd: string
  share_percent: string
}

export type OrganizationInvoice = {
  period: OrganizationInvoicePeriod
  currency: 'USD'
  accounts: OrganizationInvoiceAccount[]
  category_rows: OrganizationInvoiceCategoryRow[]
  model_rows: OrganizationInvoiceModelRow[]
  gross_total_quota: number
  gross_total_amount_usd: string
  settled_total_amount_usd: string
}

export type OrganizationSettlementRuleOption = {
  category_key: string
  category_name: string
  fallback: boolean
  models: string[]
  factor: string
  factor_scaled: number
  effective_month: string
  source_effective_month?: string
  version: number
  inherited: boolean
}

export type OrganizationSettlementRuleUpdatePayload = {
  category_key: string
  factor: string
  effective_month: string
  expected_version: number
}

export type OrganizationSettlementRuleUpdateResult = {
  category_key: string
  factor: string
  factor_scaled: number
  effective_month: string
  version: number
  created_at: number
  updated_at: number
  changed: boolean
  previous_factor: string
  previous_factor_scaled: number
}
