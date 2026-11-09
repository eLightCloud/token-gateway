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
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { Button } from '@/components/ui/button'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { toIntlLocale } from '@/i18n/languages'

import type { ConfigItem } from './api'
import { formatPersonalRatio } from './format'

function modelChannelKey(item: ConfigItem): string {
  return `${item.model}:${item.channel_id}`
}

export function ResellerAgentDiscounts(props: {
  items: ConfigItem[]
  candidates: ConfigItem[]
  children: ReactNode
}) {
  const { t } = useTranslation()
  // Mount the editor on demand, then keep the draft when it is collapsed.
  const [editing, setEditing] = useState<boolean | null>(null)
  const available = new Set(props.candidates.map(modelChannelKey))
  const discounts = props.items.filter(
    (item) =>
      item.authorized &&
      Number(item.ratio) < 1 &&
      available.has(modelChannelKey(item))
  )

  return (
    <Collapsible
      open={editing === true}
      onOpenChange={setEditing}
      className='space-y-3'
    >
      <CollapsibleTrigger render={<Button variant='outline' />}>
        {editing ? t('Collapse') : t('Edit')}
      </CollapsibleTrigger>
      {!editing && <ResellerDiscountTable items={discounts} />}
      <CollapsibleContent keepMounted={editing !== null}>
        {props.children}
      </CollapsibleContent>
    </Collapsible>
  )
}

export function ResellerDiscountTable(props: { items: ConfigItem[] }) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  return (
    <StaticDataTable
      data={props.items}
      getRowKey={modelChannelKey}
      emptyContent={t('No active model discounts')}
      columns={[
        { id: 'model', header: t('Model'), cell: (row) => row.model },
        {
          id: 'channel',
          header: t('Channel'),
          cell: (row) => row.channel_id,
        },
        {
          id: 'ratio',
          header: t('Personal ratio'),
          cell: (row) => formatPersonalRatio(row.ratio, locale),
        },
      ]}
    />
  )
}
