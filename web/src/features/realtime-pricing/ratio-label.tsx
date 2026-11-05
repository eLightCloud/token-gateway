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

import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'

export function PricingRatioLabel(props: { ratio: string }) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const value = Number(props.ratio)
  const ratio = formatNumber(value, locale)
  if (value === 1) return <>{t('{{ratio}}× · Base price', { ratio })}</>
  if (value > 1) {
    return (
      <>
        {t('{{ratio}}× · {{percent}}% markup', {
          ratio,
          percent: formatNumber((value - 1) * 100, locale),
        })}
      </>
    )
  }
  if (locale?.startsWith('zh')) {
    return (
      <>
        {t('{{ratio}}× · {{discount}}/10 of base price', {
          ratio,
          discount: formatNumber(value * 10, locale),
        })}
      </>
    )
  }
  return (
    <>
      {t('{{ratio}}× · {{percent}}% discount', {
        ratio,
        percent: formatNumber((1 - value) * 100, locale),
      })}
    </>
  )
}
