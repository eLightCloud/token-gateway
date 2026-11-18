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

import { z } from 'zod'

import type { NavGroup } from '@/components/layout/types'

export const observationViews = [
  'overview',
  'analytics',
  'usage',
  'audit',
  'tasks',
] as const
export type ObservationView = (typeof observationViews)[number]

const menu = {
  overview: { title: 'Overview', source: '/dashboard/overview' },
  analytics: { title: 'Dashboard', source: '/dashboard/models' },
  usage: { title: 'Usage Logs', source: '/usage-logs/common' },
  audit: { title: 'Audit Logs', source: '/usage-logs/audit' },
  tasks: { title: 'Task Logs', source: '/usage-logs/task' },
} as const

export function observationTitle(
  view: ObservationView,
  t: (key: string) => string
) {
  return t(menu[view].title)
}

export function observationMenu(
  groups: NavGroup[],
  t: (key: string) => string
): NavGroup {
  const general = groups.find((group) => group.id === 'general')
  return {
    id: 'reseller-observation',
    title: t('General'),
    items: observationViews.map((view) => {
      const original = general?.items.find(
        (item) => 'url' in item && item.url === menu[view].source
      )
      return {
        title: observationTitle(view, t),
        icon: original?.icon,
        url: `/reseller/observe/${view}`,
      }
    }),
  }
}

export function isObservationPath(path: string) {
  return observationViews.some((view) => path === `/reseller/observe/${view}`)
}

const timestamp = z.coerce
  .number()
  .int()
  .positive()
  .max(253402300799)
  .optional()
  .catch(undefined)
export const observationSearchSchema = z.object({
  customer_id: z.coerce.number().int().positive().optional().catch(undefined),
  from: timestamp,
  until: timestamp,
  model: z.string().max(512).optional().catch(undefined),
  request_id: z.string().max(64).optional().catch(undefined),
  task_id: z.string().max(255).optional().catch(undefined),
  kind: z
    .enum(['consumption', 'failure', 'drawing', 'async'])
    .optional()
    .catch(undefined),
})

export type ObservationSearch = z.infer<typeof observationSearchSchema>

export const observationDay = 86400

export function observationDates(): Required<
  Pick<ObservationSearch, 'from' | 'until'>
> {
  const today =
    Math.floor(Date.now() / (observationDay * 1000)) * observationDay
  return { from: today - 6 * observationDay, until: today + observationDay }
}
