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
import { getRouteApi, Link } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'

import { SectionPageLayout } from '@/components/layout'
import { buttonVariants } from '@/components/ui/button'
import { RealtimePricingEditor } from '@/features/realtime-pricing/editor'
import { useAuthStore } from '@/stores/auth-store'

import { resellerKey, resellerRequest, type Workspace } from './api'
import { ResellerConfigEditor } from './config-editor'
import { ResellerQueryStatus } from './shared'

const route = getRouteApi('/_authenticated/users/')

export function UserPersonalPricingPage(props: { userID: number }) {
  const { t } = useTranslation()
  const search = route.useSearch()
  const viewer = useAuthStore((s) => s.auth.user?.id)
  const query = useQuery({
    meta: { errorToast: false },
    queryKey: resellerKey(viewer, 'ordinary-pricing', props.userID),
    queryFn: () =>
      resellerRequest<Workspace>(`/api/reseller/admin/agents/${props.userID}`),
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  })
  const username = query.data?.user?.username
  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>{t('Personal ratio')}</SectionPageLayout.Title>
      <SectionPageLayout.Actions>
        <Link
          to='/users'
          search={{ ...search, pricingUserId: undefined }}
          activeOptions={{ exact: true }}
          className={buttonVariants({ variant: 'outline' })}
        >
          {t('Back to list')}
        </Link>
      </SectionPageLayout.Actions>
      <SectionPageLayout.Content>
        <div className='space-y-4'>
          <p className='text-muted-foreground text-sm'>
            {t('User ID')} #{props.userID}
            {username && ` · ${username}`}
          </p>
          <ResellerQueryStatus
            pending={query.isPending}
            error={query.isError}
            retry={() => void query.refetch()}
          />
          {query.data && query.data.pricing_phase !== 'active' && (
            <ResellerConfigEditor
              key={query.data.config.revision}
              userID={props.userID}
              admin
              authorization={false}
              config={query.data.config}
              candidates={query.data.candidates}
              onSuccess={() => void query.refetch()}
              onReload={async () => {
                const result = await query.refetch()
                if (result.isError) throw result.error
              }}
            />
          )}
          <RealtimePricingEditor subject='user' subjectID={props.userID} />
        </div>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
