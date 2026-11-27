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

import { Alert, AlertDescription } from '@/components/ui/alert'
import type { ModelPricingExchangePreview } from '@/features/model-pricing/api'

export function BillingExchangePreview({
  preview,
}: {
  preview: ModelPricingExchangePreview
}) {
  const { t } = useTranslation()
  return (
    <Alert>
      <AlertDescription>
        <div>
          {t('Current exchange rate: 1 USD = {{rate}} {{currency}}', {
            rate: String(preview.rate),
            currency: 'CNY',
          })}
        </div>
        <div>{t('Prices converted to USD')}</div>
        <code className='block break-all'>{preview.usd_expression}</code>
      </AlertDescription>
    </Alert>
  )
}
