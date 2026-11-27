import { useQuery } from '@tanstack/react-query'
import { useEffect } from 'react'

import { api } from '@/lib/api'
import {
  getServerErrorStatus,
  requireServerSuccess,
} from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import type { PricingData } from '../types'
import { usePricingView } from './use-pricing-data'

export const MODEL_CATALOG_QUERY_KEY = ['model-catalog'] as const

type ModelCatalogData = PricingData & {
  visibility_filter?: 'domestic' | 'discounted'
}

export function useModelCatalog() {
  const userId = useAuthStore((state) => state.auth.user?.id)
  const checkingAuth = useAuthStore(
    (state) => state.auth.bootstrapState === 'checking'
  )
  const { data, isLoading, error, refetch } = useQuery({
    queryKey: [...MODEL_CATALOG_QUERY_KEY, userId ?? 'anonymous'],
    queryFn: async ({ signal }) => {
      // The query owns cancellation and deduplication across catalog consumers.
      const response = await api.get<ModelCatalogData>('/api/pricing/catalog', {
        signal,
        disableDuplicate: true,
      })
      return requireServerSuccess(response.data)
    },
    enabled: !checkingAuth,
    refetchOnWindowFocus: 'always',
    refetchOnMount: 'always',
  })

  useEffect(() => {
    if (checkingAuth) return

    // Between two visible windows, focus can change without a visibility event.
    const refreshOnFocus = () => {
      void refetch({ cancelRefetch: false })
    }
    window.addEventListener('focus', refreshOnFocus)
    return () => window.removeEventListener('focus', refreshOnFocus)
  }, [checkingAuth, refetch])

  const status = getServerErrorStatus(error)
  const visibleData = status === 401 || status === 403 ? undefined : data

  return {
    ...usePricingView(visibleData),
    discountedOnly: visibleData?.visibility_filter === 'discounted',
    isLoading: checkingAuth || isLoading,
    error,
    refetch,
  }
}
