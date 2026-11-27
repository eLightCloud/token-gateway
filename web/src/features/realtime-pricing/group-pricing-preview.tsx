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

import { StaticDataTable } from '@/components/data-table'

import type { PricingPreview } from './api'

export function GroupPricingPreview(props: {
  groups: PricingPreview['group_status']
}) {
  const { t } = useTranslation()
  if (!props.groups?.length) return null
  return (
    <StaticDataTable
      data={props.groups}
      getRowKey={(row) => JSON.stringify([row.model, row.group])}
      columns={[
        { id: 'model', header: t('Model'), cell: (row) => row.model },
        { id: 'group', header: t('Group'), cell: (row) => row.group },
        {
          id: 'availability',
          header: t('Effective status'),
          cell: (row) => (row.available ? t('Effective') : t('Unavailable')),
        },
      ]}
    />
  )
}
