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

import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { toIntlLocale } from '@/i18n/languages'
import { formatQuotaWithCurrency } from '@/lib/currency'

import type { ResellerAccount } from './api'

export function ResellerQueryStatus({
  pending,
  error,
  retry,
}: {
  pending: boolean
  error: boolean
  retry: () => void
}) {
  const { t } = useTranslation()
  if (pending) return <p role='status'>{t('Loading...')}</p>
  if (error) {
    return (
      <div role='alert' className='flex items-center gap-3'>
        <p>{t('Failed to load data. Previous data may be outdated.')}</p>
        <Button variant='outline' onClick={retry}>
          {t('Retry')}
        </Button>
      </div>
    )
  }
  return null
}

export function ResellerTimestamp({ value }: { value: number }) {
  const { t, i18n } = useTranslation()
  return (
    <span>
      {value
        ? new Date(value * 1000).toLocaleString(
            toIntlLocale(i18n.resolvedLanguage || i18n.language)
          )
        : t('Pending posting')}
    </span>
  )
}

export function ProcurementSummary({ account }: { account: ResellerAccount }) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  return (
    <div className='space-y-3'>
      <div className='grid gap-3 sm:grid-cols-3'>
        {[
          {
            title: t('Available procurement quota'),
            value: account.balance - account.reserved,
          },
          { title: t('Processing reservations'), value: account.reserved },
          { title: t('Settled procurement balance'), value: account.balance },
        ].map((item) => (
          <Card key={item.title}>
            <CardHeader>
              <CardTitle>{item.title}</CardTitle>
            </CardHeader>
            <CardContent className='text-xl font-semibold tabular-nums'>
              {formatQuotaWithCurrency(item.value, { locale })}
            </CardContent>
          </Card>
        ))}
      </div>
      <p className='text-muted-foreground text-sm'>
        {t('Accounting updated at')}{' '}
        <ResellerTimestamp value={account.published_at} />.{' '}
        {t('Pending posting does not interrupt requests.')}
      </p>
      {account.balance <= account.reserved && (
        <p role='status'>
          {t(
            'Procurement quota is insufficient. New customer requests are restricted.'
          )}
        </p>
      )}
    </div>
  )
}
