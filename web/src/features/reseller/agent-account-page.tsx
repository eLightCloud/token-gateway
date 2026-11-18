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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { SectionPageLayout } from '@/components/layout'
import { Input } from '@/components/ui/input'
import { useAuthStore } from '@/stores/auth-store'

import { ResellerProcurementRatioTable } from './agent-discounts'
import { ResellerAgentInformation } from './agent-information'
import {
  resellerKey,
  resellerOwnAccountPath,
  resellerRequest,
  type ResellerOwnAccount,
} from './api'
import { ResellerQueryStatus } from './shared'

function useResellerOwnAccount() {
  const viewer = useAuthStore((s) => s.auth.user?.id)
  return useQuery({
    meta: { errorToast: false },
    queryKey: resellerKey(viewer, 'own-account'),
    queryFn: () => resellerRequest<ResellerOwnAccount>(resellerOwnAccountPath),
    enabled: viewer !== undefined,
  })
}

export function ResellerOwnProfile(props: { onSaved?: () => void }) {
  const query = useResellerOwnAccount()
  if (query.data) {
    return (
      <ResellerAgentInformation
        key={query.data.user.user_id}
        user={query.data.user}
        onSaved={() => {
          void query.refetch()
          props.onSaved?.()
        }}
      />
    )
  }
  return (
    <ResellerQueryStatus
      pending={query.isPending}
      error={query.isError}
      retry={() => void query.refetch()}
    />
  )
}

export function ResellerOwnProcurementRatiosPage() {
  const { t } = useTranslation()
  const [search, setSearch] = useState('')
  const query = useResellerOwnAccount()
  const filter = search.trim().toLowerCase()
  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>
        {t('My procurement ratios')}
      </SectionPageLayout.Title>
      <SectionPageLayout.Content>
        <ResellerQueryStatus
          pending={query.isPending}
          error={query.isError}
          retry={() => void query.refetch()}
        />
        {query.data && (
          <div className='space-y-4'>
            <Input
              className='max-w-xs'
              aria-label={t('Search models')}
              placeholder={t('Search models')}
              value={search}
              onChange={(event) => setSearch(event.target.value)}
            />
            <ResellerProcurementRatioTable
              items={query.data.procurement_ratios.filter((item) =>
                item.model.toLowerCase().includes(filter)
              )}
              filtered={Boolean(filter)}
            />
          </div>
        )}
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
