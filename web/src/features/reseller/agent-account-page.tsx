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
import { useTranslation } from 'react-i18next'

import { SectionPageLayout } from '@/components/layout'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useAuthStore } from '@/stores/auth-store'

import { ResellerDiscountTable } from './agent-discounts'
import { ResellerAgentInformation } from './agent-information'
import {
  resellerKey,
  resellerOwnAccountPath,
  resellerRequest,
  type ResellerOwnAccount,
} from './api'
import { ProcurementSummary, ResellerQueryStatus } from './shared'

export function ResellerOwnAccountPage() {
  const { t } = useTranslation()
  const viewer = useAuthStore((s) => s.auth.user?.id)
  const query = useQuery({
    meta: { errorToast: false },
    queryKey: resellerKey(viewer, 'own-account'),
    queryFn: () => resellerRequest<ResellerOwnAccount>(resellerOwnAccountPath),
    enabled: viewer !== undefined,
  })
  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>{t('Account details')}</SectionPageLayout.Title>
      <SectionPageLayout.Content>
        <ResellerQueryStatus
          pending={query.isPending}
          error={query.isError}
          retry={() => void query.refetch()}
        />
        {query.data && (
          <div className='space-y-4'>
            {query.data.user.account && (
              <ProcurementSummary account={query.data.user.account} />
            )}
            <Tabs defaultValue='information'>
              <TabsList>
                <TabsTrigger value='information'>
                  {t('Basic information')}
                </TabsTrigger>
                <TabsTrigger value='configuration'>
                  {t('Model discounts')}
                </TabsTrigger>
              </TabsList>
              <TabsContent value='information'>
                <ResellerAgentInformation
                  key={viewer}
                  user={query.data.user}
                  onSaved={() => void query.refetch()}
                />
              </TabsContent>
              <TabsContent value='configuration' className='pt-2'>
                <ResellerDiscountTable items={query.data.discounts} />
              </TabsContent>
            </Tabs>
          </div>
        )}
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
