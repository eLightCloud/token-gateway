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
import type { ResellerUser } from './api'
import { resellerSearchSchema } from './search'

export function hasResellerAgentPassword(activation: string): boolean {
  return activation === 'active' || activation === 'security_pending'
}

export function isResellerAgentActivated(user: ResellerUser): boolean {
  return (
    !user.platform_disabled && (user.account?.enabled ?? !user.agent_disabled)
  )
}

export const resellerAgentSearchSchema = resellerSearchSchema.transform(
  (search) => {
    const activation = hasResellerAgentPassword(search.activation ?? '')
      ? 'password_set'
      : search.activation
    const serviceStatus =
      search.service_status === 'platform_disabled' ||
      search.service_status === 'agent_disabled'
        ? 'disabled'
        : search.service_status
    if (
      activation === search.activation &&
      serviceStatus === search.service_status
    ) {
      return search
    }
    return {
      ...search,
      activation,
      service_status: serviceStatus,
      after: undefined,
    }
  }
)
