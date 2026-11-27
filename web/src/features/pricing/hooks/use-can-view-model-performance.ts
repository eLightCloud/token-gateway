import { useAuthStore } from '@/stores/auth-store'

export function useCanViewModelPerformance() {
  return useAuthStore((state) => {
    const kind = state.auth.user?.reseller_kind
    return kind !== 'agent' && kind !== 'agent_customer'
  })
}
