import { createFileRoute, redirect } from '@tanstack/react-router'
import { z } from 'zod'

import { Reconciliation } from '@/features/reconciliation'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

const searchSchema = z.object({
  start: z.number().int().nonnegative().optional().catch(undefined),
  end: z.number().int().positive().optional().catch(undefined),
  perspective: z
    .enum(['customer', 'upstream', 'api_address'])
    .optional()
    .catch(undefined),
  dimension: z
    .enum([
      'user',
      'user_model',
      'user_channel',
      'channel',
      'channel_model',
      'upstream_channel',
      'upstream_channel_model',
    ])
    .optional()
    .catch(undefined),
  billType: z.enum(['consume', 'refund']).optional().catch(undefined),
  organizationId: z.number().int().positive().optional().catch(undefined),
  modelName: z.string().optional().catch(undefined),
  channelId: z.number().int().positive().optional().catch(undefined),
  requestId: z.string().optional().catch(undefined),
  page: z.number().int().positive().optional().catch(undefined),
})

export const Route = createFileRoute('/_authenticated/reconciliation/')({
  beforeLoad: () => {
    const { auth } = useAuthStore.getState()
    if (auth.user?.role !== ROLE.SUPER_ADMIN) throw redirect({ to: '/403' })
  },
  validateSearch: searchSchema,
  component: Reconciliation,
})
