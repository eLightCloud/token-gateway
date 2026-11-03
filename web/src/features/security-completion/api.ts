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
import type { AccountSecurityResult } from '@/features/profile/types'
import { api } from '@/lib/api'
import { authRequestOptions, authResult } from '@/lib/secure-verification'

export interface SecurityCompletion {
  required: boolean
  email_verified: boolean
  mfa_enabled: boolean
  has_password: boolean
  complete: boolean
  kind: string
  email: string
  activation: string
}

export function getSecurityCompletion(): Promise<SecurityCompletion> {
  return authResult(api.get('/api/security/completion', authRequestOptions))
}

export function startSecurityEmail(): Promise<null> {
  return authResult(
    api.post('/api/security/email/start', {}, authRequestOptions)
  )
}

export function confirmSecurityEmail(
  token: string
): Promise<AccountSecurityResult> {
  return authResult(
    api.post(
      '/api/security/email/complete',
      { token },
      {
        ...authRequestOptions,
        acceptAuthRotation: true,
        singleUseAuthorization: true,
      }
    )
  )
}

export function replaceSecurityFactor(
  proof: string,
  signal: AbortSignal
): Promise<AccountSecurityResult> {
  return authResult(
    api.post(
      '/api/security/factor/replace',
      {},
      {
        ...authRequestOptions,
        headers: { 'X-Security-Proof': proof },
        signal,
        acceptAuthRotation: true,
        singleUseAuthorization: true,
      }
    )
  )
}
