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
import { getRouteApi } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'

import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'

const route = getRouteApi('/_authenticated/users/')
export function useResellerAdminUserFilter() {
  const { t } = useTranslation()
  const search = route.useSearch()
  const navigate = route.useNavigate()
  const kind = search.reseller_kind ?? ''
  const agentID = search.agent_id ?? ''
  return {
    active: Boolean(kind || agentID),
    params: { reseller_kind: kind, agent_id: agentID },
    controls: (
      <div className='flex flex-wrap gap-2'>
        <div className='space-y-1'>
          <Label htmlFor='user-reseller-kind'>{t('Account type')}</Label>
          <NativeSelect
            id='user-reseller-kind'
            value={kind}
            onChange={(event) => {
              void navigate({
                search: (previous) => ({
                  ...previous,
                  reseller_kind: event.target.value,
                  page: 1,
                }),
                replace: true,
              })
            }}
          >
            <option value=''>{t('All')}</option>
            <option value='ordinary'>{t('Ordinary user')}</option>
            <option value='agent'>{t('Reseller')}</option>
            <option value='agent_customer'>{t('Reseller customer')}</option>
          </NativeSelect>
        </div>
        <div className='space-y-1'>
          <Label htmlFor='user-parent-agent'>{t('Parent reseller ID')}</Label>
          <Input
            id='user-parent-agent'
            className='w-36'
            type='number'
            min='1'
            value={agentID}
            onChange={(event) => {
              void navigate({
                search: (previous) => ({
                  ...previous,
                  agent_id: event.target.value,
                  page: 1,
                }),
                replace: true,
              })
            }}
          />
        </div>
      </div>
    ),
  }
}
