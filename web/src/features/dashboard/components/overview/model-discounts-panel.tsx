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
import { useQuery } from '@tanstack/react-query'
import { Tag } from 'lucide-react'
import { useId, useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  StaticDataTable,
  staticDataTableClassNames,
  type StaticDataTableColumn,
} from '@/components/data-table'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { IconBadge } from '@/components/ui/icon-badge'
import type { RatioRange } from '@/features/reseller/api'
import { formatPersonalRatio } from '@/features/reseller/format'
import { personalRatioQuery } from '@/features/reseller/personal-ratio-query'
import { ResellerQueryStatus } from '@/features/reseller/shared'
import { toIntlLocale } from '@/i18n/languages'

import { PanelWrapper } from '../ui/panel-wrapper'

const PREVIEW_LIMIT = 10

export function ModelDiscountsPanel(props: { userID: number | undefined }) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const useChineseDiscount = locale?.startsWith('zh')
  const [expanded, setExpanded] = useState(false)
  const titleID = useId()
  const tableID = useId()
  const query = useQuery({
    ...personalRatioQuery(props.userID),
    meta: { errorToast: false },
  })
  const discounts = query.isError
    ? []
    : (query.data?.filter((row) => Number(row.min) < 1) ?? [])
  const columns: StaticDataTableColumn<RatioRange>[] = [
    {
      id: 'model',
      header: t('Model'),
      cellClassName: 'font-medium',
      cell: (row) => (
        <span className='break-all whitespace-normal'>{row.model}</span>
      ),
    },
    {
      id: 'discount',
      header: t('Active discount'),
      className: 'w-[44%] text-right',
      cellClassName: 'text-right',
      cell: (row) => {
        const min = Number(row.min)
        const max = Number(row.max)
        const partial = max >= 1
        const formatDiscount = (ratio: number) =>
          formatPersonalRatio(
            String(useChineseDiscount ? ratio * 10 : (1 - ratio) * 100),
            locale
          )
        const best = formatDiscount(min)
        let discount = best
        if (!partial && min !== max) {
          const least = formatDiscount(max)
          discount = useChineseDiscount
            ? `${best}–${least}`
            : `${least}–${best}`
        }
        const label = useChineseDiscount
          ? t('{{discount}}/10 of base price', { discount })
          : t('{{discount}}% off', { discount })

        return (
          <Badge
            variant='secondary'
            className='bg-success/10 text-success h-auto max-w-full text-right whitespace-normal tabular-nums'
          >
            {partial ? t('Up to {{discount}}', { discount: label }) : label}
          </Badge>
        )
      },
    },
  ]

  return (
    <PanelWrapper
      title={
        <h3 id={titleID} className='flex items-center gap-2'>
          <IconBadge tone='success' size='sm'>
            <Tag />
          </IconBadge>
          {t('My model discounts')}
        </h3>
      }
      description={
        discounts.length > 1
          ? t('{{count}} models', { count: discounts.length })
          : undefined
      }
      loading={query.isLoading}
      empty={!query.isError && discounts.length === 0}
      emptyMessage={t('No active model discounts')}
      height='h-24'
      contentClassName={query.isError ? undefined : 'p-0'}
      headerActions={
        discounts.length > PREVIEW_LIMIT && (
          <Button
            variant='ghost'
            size='sm'
            aria-expanded={expanded}
            aria-controls={tableID}
            onClick={() => setExpanded((current) => !current)}
          >
            {expanded ? t('Collapse') : t('View all')}
          </Button>
        )
      }
    >
      {query.isError ? (
        <ResellerQueryStatus
          pending={query.isFetching}
          error
          retry={() => void query.refetch()}
        />
      ) : (
        <StaticDataTable
          className={staticDataTableClassNames.embeddedContainer}
          tableClassName='table-fixed'
          tableProps={{ id: tableID, 'aria-labelledby': titleID }}
          columns={columns}
          data={expanded ? discounts : discounts.slice(0, PREVIEW_LIMIT)}
          getRowKey={(row) => row.model}
        />
      )}
    </PanelWrapper>
  )
}
