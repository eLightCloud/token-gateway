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
import {
  PersonalPricingChannels,
  PersonalPricingRatio,
} from './personal-rule-editor'
import { PricingRatioLabel } from './ratio-label'
import { PricingRuleSwitch } from './rule-switch'
import type { PersonalRuleEdit } from './use-personal-rule-editor'

export function PricingRulesTable(props: {
  rules: PricingRule[]
  candidates: PricingWorkspace['candidates']
  disabled: boolean
  emptyDescription?: string
  inlineEdit?: PersonalRuleEdit
  onEdit: (rule: PricingRule) => void
  onDelete: (model: string) => void
  onToggle?: (model: string, enabled: boolean) => void
}) {
  const { t } = useTranslation()
  const mobile = useIsMobile()
  const candidates = props.candidates
  const disabled = props.disabled
  const inlineEdit = props.inlineEdit
  const onEdit = props.onEdit
  const onDelete = props.onDelete
  const onToggle = props.onToggle
  const columns = useMemo<ColumnDef<PricingRule>[]>(
    () => [
      {
        id: 'model',
        header: t('Model'),
        meta: { mobileTitle: true },
        cell: ({ row }) => (
          <span className='break-all whitespace-normal'>
            {row.original.model}
          </span>
        ),
      },
      {
        id: 'channels',
        header: t('Applicable channels'),
        cell: ({ row }) =>
          inlineEdit?.model === row.original.model ? (
            <PersonalPricingChannels
              edit={inlineEdit}
              candidates={candidates}
              disabled={disabled}
            />
          ) : (
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
        cell: ({ row }) =>
          inlineEdit?.model === row.original.model ? (
            <PersonalPricingRatio edit={inlineEdit} disabled={disabled} />
          ) : (
            <PricingRatioLabel ratio={row.original.ratio} />
          ),
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
          let availability = t('Available')
          if (available === 0) availability = t('No available channels')
          else if (available < row.original.channel_ids.length) {
            availability = t('Some channels unavailable')
          }
          return (
            <div className='space-y-1'>
              {onToggle && (
                <PricingRuleSwitch
                  rule={row.original}
                  disabled={disabled || !!inlineEdit}
                  onToggle={onToggle}
                />
              )}
              {onToggle && row.original.disabled && <p>{t('Inherited')}</p>}
              <p>{availability}</p>
            </div>
          )
        },
      },
      {
        id: 'actions',
        header: t('Actions'),
        cell: ({ row }) =>
          inlineEdit?.model === row.original.model ? (
            <Button
              variant='ghost'
              disabled={disabled}
              onClick={inlineEdit.cancel}
            >
              {t('Cancel')}
            </Button>
          ) : (
            <div className='flex gap-2'>
              <Button
                variant='outline'
                disabled={disabled || !!inlineEdit}
                onClick={() => onEdit(row.original)}
              >
                {t('Edit')}
              </Button>
              <Button
                variant='ghost'
                disabled={disabled || !!inlineEdit}
                onClick={() => onDelete(row.original.model)}
              >
                {t('Delete')}
              </Button>
            </div>
          ),
      },
    ],
    [candidates, disabled, inlineEdit, onEdit, onDelete, onToggle, t]
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
      <MobileCardList
        table={table}
        emptyTitle={t('No models configured')}
        emptyDescription={props.emptyDescription}
      />
    )
  }
  return (
    <DataTableView
      table={table}
      emptyTitle={t('No models configured')}
      emptyDescription={props.emptyDescription}
    />
  )
}
