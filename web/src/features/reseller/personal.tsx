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
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { TwoFACard } from '@/features/security/components/two-fa-card'
import { toIntlLocale } from '@/i18n/languages'
import { getSelf } from '@/lib/api'
import { formatQuotaWithCurrency } from '@/lib/currency'
import { useAuthStore } from '@/stores/auth-store'

import { resellerKey, resellerRequest, type RatioRange } from './api'
import { formatPersonalRatio } from './format'
import { ResellerQueryStatus, ResellerTimestamp } from './shared'

export function PersonalRatioBadge({ model }: { model: string }) {
  const user = useAuthStore((s) => s.auth.user)
  if (!user || user.reseller_kind === 'agent') return null
  return <AuthenticatedPersonalRatioBadge model={model} userID={user.id} />
}

function AuthenticatedPersonalRatioBadge({
  model,
  userID,
}: {
  model: string
  userID: number
}) {
  const query = useQuery({
    queryKey: resellerKey(userID, 'ratios'),
    queryFn: () =>
      resellerRequest<RatioRange[]>('/api/reseller/personal/ratios'),
    staleTime: 30_000,
  })
  const range = query.data?.find((item) => item.model === model)
  if (!range || (range.min === '1' && range.max === '1')) return null
  return (
    <span className='text-muted-foreground text-xs'>
      <PersonalRatioLabel min={range.min} max={range.max} />
    </span>
  )
}

export function ResellerWallet() {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const userID = useAuthStore((s) => s.auth.user?.id)
  const query = useQuery({
    meta: { errorToast: false },
    queryKey: resellerKey(userID, 'wallet'),
    queryFn: getSelf,
  })
  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>{t('Quota')}</SectionPageLayout.Title>
      <SectionPageLayout.Content>
        <ResellerQueryStatus
          pending={query.isPending}
          error={query.isError}
          retry={() => void query.refetch()}
        />
        <div className='space-y-4'>
          {query.data?.data && (
            <Card>
              <CardHeader>
                <CardTitle>{t('Available quota')}</CardTitle>
              </CardHeader>
              <CardContent>
                <p className='text-2xl tabular-nums'>
                  {formatQuotaWithCurrency(query.data.data.quota, { locale })}
                </p>
                <p className='text-muted-foreground mt-3 text-sm'>
                  {t(
                    'Contact your reseller to adjust quota. Consumption and refunds appear in your usage logs.'
                  )}
                </p>
              </CardContent>
            </Card>
          )}
          <PersonalPricingHistory />
        </div>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}

export function ResellerSecurity() {
  const { t } = useTranslation()
  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>{t('Account Security')}</SectionPageLayout.Title>
      <SectionPageLayout.Content>
        <TwoFACard loading={false} required />
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}

export function PersonalPricingHistory() {
  const { t } = useTranslation()
  const userID = useAuthStore((s) => s.auth.user?.id)
  const query = useQuery({
    meta: { errorToast: false },
    queryKey: resellerKey(userID, 'ratio-history'),
    queryFn: () =>
      resellerRequest<
        { revision: number; created_at: number; models: RatioRange[] }[]
      >('/api/reseller/personal/history'),
  })
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('Personal ratio history')}</CardTitle>
      </CardHeader>
      <CardContent className='space-y-3'>
        <ResellerQueryStatus
          pending={query.isPending}
          error={query.isError}
          retry={() => void query.refetch()}
        />
        {query.data?.length === 0 && <p>{t('No results')}</p>}
        {query.data?.map((entry) => (
          <div key={entry.revision} className='space-y-1 border-b pb-3'>
            <ResellerTimestamp value={entry.created_at} />
            {entry.models.map((model) => (
              <p key={model.model}>
                {model.model}:{' '}
                <PersonalRatioLabel min={model.min} max={model.max} />
              </p>
            ))}
          </div>
        ))}
      </CardContent>
    </Card>
  )
}

export function OrdinaryPersonalPricingHistory() {
  return (
    <div className='px-4 pb-6 md:px-6'>
      <PersonalPricingHistory />
    </div>
  )
}

function PersonalRatioLabel(props: { min: string; max: string }) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const ratio =
    props.min === props.max
      ? formatPersonalRatio(props.min, locale)
      : `${formatPersonalRatio(props.min, locale)}–${formatPersonalRatio(props.max, locale)}`
  if (Number(props.max) < 1) {
    return <>{t('Personal price {{ratio}}× (discount)', { ratio })}</>
  }
  if (Number(props.min) > 1) {
    return <>{t('Personal price {{ratio}}× (markup)', { ratio })}</>
  }
  return <>{t('Personal ratio {{ratio}}', { ratio })}</>
}
