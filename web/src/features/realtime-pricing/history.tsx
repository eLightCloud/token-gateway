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
import { useTranslation } from 'react-i18next'

import { ResellerTimestamp } from '@/features/reseller/shared'

import type { PricingWorkspace } from './api'
import { PricingRatioLabel } from './ratio-label'

export function PricingHistory(props: {
  entries: PricingWorkspace['history']
  candidates: PricingWorkspace['candidates']
}) {
  const { t } = useTranslation()
  return (
    <div className='space-y-3'>
      {props.entries.map((entry, index) => {
        const previous =
          entry.previous_items ?? props.entries[index + 1]?.items ?? []
        const names = [
          ...new Set([...entry.items, ...previous].map((r) => r.model)),
        ].sort()
        const changes = names
          .map((model) => {
            const before = previous.find((r) => r.model === model)
            const after = entry.items.find((r) => r.model === model)
            const added =
              after?.channel_ids.filter(
                (id) => !before?.channel_ids.includes(id)
              ) ?? []
            const removed =
              before?.channel_ids.filter(
                (id) => !after?.channel_ids.includes(id)
              ) ?? []
            return { model, before, after, added, removed }
          })
          .filter(
            (c) =>
              c.before?.ratio !== c.after?.ratio ||
              c.added.length ||
              c.removed.length
          )
        return (
          <article key={entry.id} className='space-y-2 rounded border p-3'>
            <p>
              #{entry.revision} ·{' '}
              {entry.operator_name || `#${entry.operator_id}`} ·{' '}
              <ResellerTimestamp value={entry.created_at} />
            </p>
            {changes.length === 0 && <p>{t('No pricing changes')}</p>}
            {changes.map((change) => (
              <div key={change.model} className='space-y-1 border-t pt-2'>
                <p className='font-medium break-all'>{change.model}</p>
                <p>
                  {t('Ratio')}:{' '}
                  {change.before ? (
                    <PricingRatioLabel ratio={change.before.ratio} />
                  ) : (
                    t('Inherited')
                  )}{' '}
                  →{' '}
                  {change.after ? (
                    <PricingRatioLabel ratio={change.after.ratio} />
                  ) : (
                    t('Inherited')
                  )}
                </p>
                {[
                  { label: t('Channels added'), ids: change.added },
                  { label: t('Channels removed'), ids: change.removed },
                ]
                  .filter((group) => group.ids.length > 0)
                  .map((group) => (
                    <p key={group.label} className='break-all'>
                      {group.label}:{' '}
                      {group.ids
                        .map(
                          (id) =>
                            `${props.candidates.find((c) => c.id === id)?.name || ''} #${id}`
                        )
                        .join(', ')}
                    </p>
                  ))}
              </div>
            ))}
          </article>
        )
      })}
    </div>
  )
}
