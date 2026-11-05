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
import type { TFunction } from 'i18next'

export function pricingSourceLabel(source: string, t: TFunction): string {
  if (source === 'user') {
    return t('User override')
  }
  if (source === 'organization') {
    return t('Organization inherited')
  }
  if (source === 'system') return t('System default')
  return t('Unknown pricing source')
}

export function pricingMigrationReason(reason: string, t: TFunction): string {
  if (reason === 'multiple ratios for one model') {
    return t('This model has multiple legacy ratios.')
  }
  if (reason === 'ratio must be in (0, 5] with at most two decimal places') {
    return t(
      'The legacy ratio must be greater than 0 and at most 5, with up to two decimal places.'
    )
  }
  return t('Migration conflict')
}
