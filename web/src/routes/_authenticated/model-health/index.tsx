import { createFileRoute, redirect } from '@tanstack/react-router'

import { ModelHealth } from '@/features/model-health'
import { ROLE } from '@/lib/roles'
import { statusQueryOptions } from '@/lib/status-query'
import { useAuthStore } from '@/stores/auth-store'

export const Route = createFileRoute('/_authenticated/model-health/')({
  beforeLoad: async ({ context }) => {
    if ((useAuthStore.getState().auth.user?.role ?? 0) < ROLE.ADMIN) {
      throw redirect({ to: '/403' })
    }
    const status = await context.queryClient.fetchQuery(statusQueryOptions)
    if (status?.model_health_enabled !== true) {
      throw redirect({ to: '/404' })
    }
  },
  component: ModelHealth,
})
