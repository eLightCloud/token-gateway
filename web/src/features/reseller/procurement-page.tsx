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

import { StaticDataTable } from '@/components/data-table'
import { SectionPageLayout } from '@/components/layout'
import { Button } from '@/components/ui/button'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { toIntlLocale } from '@/i18n/languages'
import { api } from '@/lib/api'
import { formatQuotaWithCurrency } from '@/lib/currency'
import { useAuthStore } from '@/stores/auth-store'

import {
  resellerKey,
  resellerRequest,
  type Adjustment,
  type ResellerAccount,
} from './api'
import { ResellerBillingTable } from './billing-page'
import {
  ProcurementSummary,
  ResellerQueryStatus,
  ResellerTimestamp,
} from './shared'

function ProcurementAdjustments() {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const userID = useAuthStore((s) => s.auth.user?.id)
  const [after, setAfter] = useState(0)
  const [until, setUntil] = useState(() => Math.floor(Date.now() / 1000) + 1)
  const [exportError, setExportError] = useState(false)
  const query = useQuery({
    meta: { errorToast: false },
    queryKey: resellerKey(userID, 'procurement-adjustments', after, until),
    queryFn: () =>
      resellerRequest<{
        account: ResellerAccount
        items: Adjustment[]
        next_cursor: number
        until: number
      }>(`/api/reseller/self/procurement?after=${after}&until=${until}`),
  })
  return (
    <div className='space-y-4'>
      {query.data && <ProcurementSummary account={query.data.account} />}
      <div className='flex gap-2'>
        <Button
          variant='outline'
          onClick={() => {
            setAfter(0)
            setUntil(Math.floor(Date.now() / 1000) + 1)
            void query.refetch()
          }}
        >
          {t('Refresh')}
        </Button>
        <Button
          variant='outline'
          onClick={async () => {
            setExportError(false)
            try {
              const response = await api.get(
                `/api/reseller/self/procurement/export?until=${until}`,
                { responseType: 'blob' }
              )
              const url = URL.createObjectURL(response.data as Blob)
              const link = document.createElement('a')
              link.href = url
              link.download = 'reseller-procurement.csv'
              link.click()
              URL.revokeObjectURL(url)
            } catch {
              setExportError(true)
            }
          }}
        >
          {t('Export CSV')}
        </Button>
      </div>
      {exportError && <p role='alert'>{t('Export failed')}</p>}
      <ResellerQueryStatus
        pending={query.isPending}
        error={query.isError}
        retry={() => void query.refetch()}
      />
      {query.data && (
        <StaticDataTable
          data={query.data.items}
          getRowKey={(row) => row.id}
          emptyContent={t('No results')}
          columns={[
            {
              id: 'operation',
              header: t('Operation ID'),
              cell: (row) => row.operation_id,
            },
            {
              id: 'mode',
              header: t('Type'),
              cell: (row) => (row.mode === 'add' ? t('Add') : t('Subtract')),
            },
            {
              id: 'amount',
              header: t('Amount'),
              cell: (row) => formatQuotaWithCurrency(row.quota, { locale }),
            },
            {
              id: 'before',
              header: t('Before'),
              cell: (row) => formatQuotaWithCurrency(row.before, { locale }),
            },
            {
              id: 'after',
              header: t('After'),
              cell: (row) => formatQuotaWithCurrency(row.after, { locale }),
            },
            { id: 'reason', header: t('Reason'), cell: (row) => row.reason },
            {
              id: 'time',
              header: t('Business time'),
              cell: (row) => <ResellerTimestamp value={row.occurred_at} />,
            },
          ]}
        />
      )}
      <div className='flex gap-2'>
        <Button variant='outline' disabled={!after} onClick={() => setAfter(0)}>
          {t('First page')}
        </Button>
        <Button
          variant='outline'
          disabled={!query.data?.next_cursor}
          onClick={() => setAfter(query.data?.next_cursor ?? 0)}
        >
          {t('Next page')}
        </Button>
      </div>
    </div>
  )
}

export function ResellerProcurementPage() {
  const { t } = useTranslation()
  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>
        {t('Procurement ledger')}
      </SectionPageLayout.Title>
      <SectionPageLayout.Content>
        <Tabs defaultValue='events'>
          <TabsList>
            <TabsTrigger value='events'>
              {t('Consumption and reservations')}
            </TabsTrigger>
            <TabsTrigger value='adjustments'>
              {t('Quota adjustments')}
            </TabsTrigger>
          </TabsList>
          <TabsContent value='events'>
            <ResellerBillingTable procurement />
          </TabsContent>
          <TabsContent value='adjustments'>
            <ProcurementAdjustments />
          </TabsContent>
        </Tabs>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
