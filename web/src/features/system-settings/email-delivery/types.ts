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
export type EmailOptions = {
  auth_mode: 'legacy' | 'microsoft_oauth2_app'
  legacy: {
    host: string
    port: number
    account: string
    from: string
    ssl: boolean
    starttls: boolean
    insecure_skip_verify: boolean
    force_login: boolean
  }
  microsoft: { tenant_id: string; client_id: string; mailbox: string }
}
export type CredentialAction = 'keep' | 'replace' | 'clear'
export type EmailIntent = {
  expected_revision: number
  options: EmailOptions
  legacy_password_action: CredentialAction
  client_secret_action: CredentialAction
}
export type EmailUpdate = {
  intent: EmailIntent
  legacy_password: string
  client_secret: string
}
export type EmailSettings = {
  revision: number
  options: EmailOptions
  legacy_password_configured: boolean
  client_secret_configured: boolean
  storage_key_ready: boolean
}
export type EmailTestIntent = { expected_revision: number; recipient: string }
export type EmailReport = {
  revision: number
  timestamp: number
  request_id: string
  reason?: 'SMTP_AUTH_DISABLED'
  code: 'EMAIL_ACCEPTED' | 'EMAIL_DELIVERY_FAILED' | 'EMAIL_DELIVERY_UNKNOWN'
  stages: {
    name: 'config' | 'token' | 'connect_tls' | 'auth' | 'submit'
    status: 'passed' | 'failed' | 'skipped' | 'unknown'
  }[]
}
