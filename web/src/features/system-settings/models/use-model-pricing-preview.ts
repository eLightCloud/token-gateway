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

import { previewModelPricing } from '@/features/model-pricing/api'
import {
  pricingFromDraft,
  type PricingValues,
} from '@/features/model-pricing/pricing'
import { combineBillingExpr } from '@/features/pricing/lib/billing-expr'
import { useDebounce } from '@/hooks/use-debounce'

import type { ModelPricingFormValues, PricingMode } from './model-pricing-core'

export function useModelPricingPreview(input: {
  values: ModelPricingFormValues
  pricingMode: PricingMode
  billingExpr: string
  requestRuleExpr: string
  pluginExpressions: Record<string, string>
  usdExchangeRate: number
}) {
  const {
    values,
    pricingMode,
    billingExpr,
    requestRuleExpr,
    pluginExpressions,
  } = input
  const model_name = values.name.trim()
  let pricing: PricingValues | undefined
  if (model_name && pricingMode === 'per-token') {
    try {
      pricing = pricingFromDraft({ ...values, billingMode: 'per-token' })
    } catch {
      // The editor validates incomplete numeric input before saving.
    }
  }

  const expression = combineBillingExpr(billingExpr, requestRuleExpr)
  const cnyPrice = /\bcny_to_usd\s*\(/
  if (
    model_name &&
    ((pricingMode === 'tiered_expr' && cnyPrice.test(expression)) ||
      Object.values(pluginExpressions).some((entry) => cnyPrice.test(entry)))
  ) {
    pricing ??= {}
    if (pricingMode === 'tiered_expr') {
      pricing['billing_setting.billing_mode'] = 'tiered_expr'
      pricing['billing_setting.billing_expr'] = expression
    }
    if (Object.keys(pluginExpressions).length > 0) {
      pricing['billing_setting.plugin_billing_expr'] = pluginExpressions
    }
  }

  const request = pricing ? JSON.stringify({ model_name, pricing }) : ''
  const debouncedRequest = useDebounce(request, 250)
  const preview = useQuery({
    queryKey: [
      'model-pricing-preview',
      debouncedRequest,
      input.usdExchangeRate,
    ],
    queryFn: () => previewModelPricing(JSON.parse(debouncedRequest)),
    enabled: Boolean(debouncedRequest) && debouncedRequest === request,
    retry: false,
    refetchOnWindowFocus: false,
    meta: { errorToast: false },
  })
  return {
    effectivePreview: request === debouncedRequest ? preview.data : undefined,
    previewFailed: request === debouncedRequest && preview.isError,
  }
}
