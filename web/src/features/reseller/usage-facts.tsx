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

export function ResellerUsageFacts({
  usage,
}: {
  usage: Record<string, number> | null
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  if (!usage || Object.keys(usage).length === 0) return <>—</>
  const labels: Record<string, string> = {
    input_tokens: t('Input Tokens'),
    output_tokens: t('Output Tokens'),
    image_count: t('Image count'),
    image_count_used: t('Image count'),
    image_count_returned: t('Image count'),
    seconds: t('Duration (s)'),
    duration: t('Duration (s)'),
    duration_seconds: t('Duration (s)'),
    video_seconds: t('Duration (s)'),
    units: t('Units'),
    count: t('Count'),
    tokens: t('Tokens'),
  }
  return (
    <div className='space-y-1 whitespace-normal'>
      {Object.entries(usage).map(([key, value]) => (
        <p key={key}>
          {labels[key] ?? t('Units')}: {formatNumber(value, locale)}
        </p>
      ))}
    </div>
  )
}
