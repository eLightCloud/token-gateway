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
import type { ColumnDef } from '@tanstack/react-table'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import {
  BadgeListCell,
  DataTableView,
  MobileCardList,
  useDataTable,
} from '@/components/data-table'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { useIsMobile } from '@/hooks/use-mobile'

import type { PricingRule, PricingWorkspace } from './api'
import { PricingRatioLabel } from './ratio-label'

export function PricingRulesTable(props: {
  rules: PricingRule[]
  candidates: PricingWorkspace['candidates']
  disabled: boolean
  onEdit: (rule: PricingRule) => void
  onDelete: (model: string) => void
}) {
  const { t } = useTranslation()
  const mobile = useIsMobile()
  const candidates = props.candidates
  const disabled = props.disabled
  const onEdit = props.onEdit
  const onDelete = props.onDelete
  const columns = useMemo<ColumnDef<PricingRule>[]>(
    () => [
      {
        id: 'model',
        header: t('Model'),
        meta: { mobileTitle: true },
        cell: ({ row }) => (
          <span className='break-all'>{row.original.model}</span>
        ),
      },
      {
        id: 'channels',
        header: t('Applicable channels'),
        cell: ({ row }) => (
          <BadgeListCell
            expandable
            max={2}
            expandLabel={t('Applicable channels')}
            items={row.original.channel_ids.map((id) => {
              const channel = candidates.find((c) => c.id === id)
              const available =
                channel?.status === 1 &&
                channel.models.split(',').includes(row.original.model)
              return (
                <Badge key={id} variant='outline'>
                  {channel?.name || '#'} #{id}
                  {!available && ` · ${t('Unavailable')}`}
                </Badge>
              )
            })}
          />
        ),
      },
      {
        id: 'ratio',
        header: t('Ratio'),
        meta: { mobileBadge: true },
        cell: ({ row }) => <PricingRatioLabel ratio={row.original.ratio} />,
      },
      {
        id: 'availability',
        header: t('Status'),
        cell: ({ row }) => {
          const available = row.original.channel_ids.filter((id) =>
            candidates.some(
              (c) =>
                c.id === id &&
                c.status === 1 &&
                c.models.split(',').includes(row.original.model)
            )
          ).length
          if (available === 0) return t('No available channels')
          if (available < row.original.channel_ids.length) {
            return t('Some channels unavailable')
          }
          return t('Available')
        },
      },
      {
        id: 'actions',
        header: t('Actions'),
        cell: ({ row }) => (
          <div className='flex gap-2'>
            <Button
              variant='outline'
              disabled={disabled}
              onClick={() => onEdit(row.original)}
            >
              {t('Edit')}
            </Button>
            <Button
              variant='ghost'
              disabled={disabled}
              onClick={() => onDelete(row.original.model)}
            >
              {t('Delete')}
            </Button>
          </div>
        ),
      },
    ],
    [candidates, disabled, onEdit, onDelete, t]
  )
  const { table } = useDataTable({
    data: props.rules,
    columns,
    getRowId: (row) => row.model,
    enableRowSelection: false,
    withPaginationRowModel: false,
    columnVisibilityStorageKey: false,
    columnSizingStorageKey: false,
  })
  if (mobile) {
    return (
      <MobileCardList table={table} emptyTitle={t('No models configured')} />
    )
  }
  return <DataTableView table={table} emptyTitle={t('No models configured')} />
}
