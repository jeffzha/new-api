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
import { createFileRoute, redirect, useNavigate } from '@tanstack/react-router'
import { useEffect } from 'react'

import { Main } from '@/components/layout'
import { Skeleton } from '@/components/ui/skeleton'
import { useAgentStoreAvailability } from '@/features/agent-store/availability'
import { WorkbenchEntry } from '@/features/workbench-entry'
import { isSidebarModuleEnabled } from '@/lib/nav-modules'

export const Route = createFileRoute('/_authenticated/playground/')({
  beforeLoad: () => {
    if (!isSidebarModuleEnabled('chat', 'playground')) {
      throw redirect({ to: '/dashboard' })
    }
  },
  component: PlaygroundPage,
})

function PlaygroundPage() {
  const navigate = useNavigate()
  const availability = useAgentStoreAvailability()

  useEffect(() => {
    if (availability.data?.enabled !== true) return
    void navigate({ to: '/agent-store', replace: true })
  }, [availability.data?.enabled, navigate])

  if (availability.isPending || availability.data?.enabled === true) {
    return (
      <Main>
        <div className='mx-auto w-full max-w-7xl space-y-4 py-6'>
          <Skeleton className='h-9 w-72 max-w-full' />
          <Skeleton className='h-48 w-full rounded-xl' />
        </div>
      </Main>
    )
  }

  return <WorkbenchEntry />
}
