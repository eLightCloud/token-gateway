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

import { formatPricingAmount } from '@/features/model-pricing/currency'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'

import { formatHistoricalCNY } from './historical-billing'

export type BillMoneySnapshot = {
  exchange_rate: string
  quota_per_unit: string
  amount: string
}

export function HistoricalBillAmount({
  quota,
  snapshot,
}: {
  quota: number
  snapshot?: BillMoneySnapshot
}) {
  const { t, i18n } = useTranslation()
  return (
    <>
      <span>
        {snapshot
          ? formatPricingAmount(quota / Number(snapshot.quota_per_unit))
          : t('{{amount}} quota', {
              amount: formatNumber(quota, toIntlLocale(i18n.language)),
            })}
      </span>
      <span className='text-muted-foreground block text-xs'>
        {snapshot
          ? `${formatHistoricalCNY(snapshot.amount)} · ${t('1 USD = {{rate}} CNY', { rate: snapshot.exchange_rate })}`
          : t('Historical exchange rate unavailable')}
      </span>
    </>
  )
}
