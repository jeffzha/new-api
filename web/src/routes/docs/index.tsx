/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { createFileRoute, redirect } from '@tanstack/react-router'

import { PublicLayout } from '@/components/layout'
import { DocsContent } from '@/features/docs/docs-content'
import { resolveDocumentationLink } from '@/features/docs/lib/documentation-link'
import { statusQueryOptions } from '@/lib/status-query'

export const Route = createFileRoute('/docs/')({
  beforeLoad: async ({ context }) => {
    const status = await context.queryClient.fetchQuery(statusQueryOptions)
    const target = resolveDocumentationLink(
      status?.docs_link,
      window.location.origin
    )
    if (target) {
      throw redirect({ href: target, reloadDocument: true, replace: true })
    }
  },
  component: () => (
    <PublicLayout showMainContainer={false}>
      <DocsContent />
    </PublicLayout>
  ),
})
