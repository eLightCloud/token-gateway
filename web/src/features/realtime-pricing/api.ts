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
import { resellerRequest } from '@/features/reseller/api'

export type PricingRule = {
  model: string
  ratio: string
  channel_ids: number[]
}
export type PricingConfig = { revision: number; items: PricingRule[] }
export type PricingWorkspace = {
  phase: 'legacy' | 'frozen' | 'active'
  config: PricingConfig
  candidates: { id: number; name: string; status: number; models: string }[]
  members: { id: number; name: string }[] | null
  history: {
    id: number
    revision: number
    operator_id: number
    operator_name?: string
    previous_items?: PricingRule[] | null
    created_at: number
    items: PricingRule[]
  }[]
}
export type PricingPreview = {
  rows: { model: string; channel_id: number; ratio: string; source: string }[]
  user_revision: number
  organization_revision: number
  generated_at: number
  user_id: number
  organization_id: number
}
export type PricingMigration = {
  phase: string
  conflicts: {
    user_id: number
    model: string
    channel_id: number
    ratio: string
    reason: string
  }[]
}
export const pricingPath = (subject: string, id: number) =>
  `/api/realtime-pricing/${subject}/${id}`
export const getPricing = (subject: string, id: number) =>
  resellerRequest<PricingWorkspace>(pricingPath(subject, id))
export const savePricing = (
  subject: string,
  id: number,
  expected_revision: number,
  items: PricingRule[]
) =>
  resellerRequest<PricingConfig>(pricingPath(subject, id), 'put', {
    expected_revision,
    items,
  })
export const previewPricing = (
  subject: string,
  id: number,
  items: PricingRule[],
  user_id: number
) =>
  resellerRequest<PricingPreview>(
    `${pricingPath(subject, id)}/preview`,
    'post',
    { items, user_id }
  )
export const migratePricing = (action: string) =>
  resellerRequest<PricingMigration>('/api/realtime-pricing-migration', 'post', {
    action,
  })

export function pricingRulesKey(rules: PricingRule[]): string {
  return JSON.stringify(
    rules
      .map((rule) => ({
        model: rule.model,
        ratio: Number(rule.ratio),
        channel_ids: [...rule.channel_ids].sort((a, b) => a - b),
      }))
      .sort((a, b) => a.model.localeCompare(b.model))
  )
}
