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
import { requireServerSuccess } from '@/lib/server-error-message'

export type VantageIntegrationSettings = {
  enabled: boolean
  allowed_ipv4: string[]
  secret_configured: boolean
}

export type VantageIntegrationUpdate = {
  enabled: boolean
  allowed_ipv4: string[]
  secret: string
}

type SettingsResponse = {
  success: boolean
  message?: string
  data: VantageIntegrationSettings
}

export const vantageIntegrationQueryKey = ['vantage-integration-settings']

export async function getVantageIntegrationSettings() {
  const response = await api.get<SettingsResponse>(
    '/api/option/vantage_integration'
  )
  return requireServerSuccess(response.data).data
}

export async function saveVantageIntegrationSettings(
  values: VantageIntegrationUpdate,
  proof: string
) {
  const response = await api.put<SettingsResponse>(
    '/api/option/vantage_integration',
    values,
    { headers: { 'X-Security-Proof': proof } }
  )
  return requireServerSuccess(response.data).data
}
