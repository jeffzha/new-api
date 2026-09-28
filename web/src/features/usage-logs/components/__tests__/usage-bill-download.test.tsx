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
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { CommonLogsHeaderActions } from '../common-logs-header-actions'
import { UsageLogsProvider, useLogsViewScope } from '../usage-logs-provider'

function DownloadFixture() {
  const scope = useLogsViewScope()
  return (
    <>
      {scope.canManageScope && (
        <button type='button' onClick={() => scope.setViewScope('self')}>
          Only Mine
        </button>
      )}
      <CommonLogsHeaderActions />
    </>
  )
}

function DownloadFixtureProvider() {
  return (
    <UsageLogsProvider>
      <DownloadFixture />
    </UsageLogsProvider>
  )
}

function renderDownload(initialEntry: string) {
  const root = createRootRoute()
  const auth = createRoute({ getParentRoute: () => root, id: '_authenticated' })
  const logs = createRoute({
    getParentRoute: () => auth,
    path: '/usage-logs/$section',
    component: DownloadFixtureProvider,
    validateSearch: (search: Record<string, unknown>) => search,
  })
  const router = createRouter({
    routeTree: root.addChildren([auth.addChildren([logs])]),
    history: createMemoryHistory({ initialEntries: [initialEntry] }),
  })
  render(<RouterProvider router={router} />)
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  useAuthStore.getState().auth.setUser(null)
})

it.each([
  {
    role: ROLE.ADMIN,
    endpoint: '/api/log/export',
    adminFilters: ['username=alice', 'channel=7'],
    selfScope: false,
  },
  {
    role: ROLE.ADMIN,
    endpoint: '/api/log/self/export',
    adminFilters: [],
    selfScope: true,
  },
  {
    role: ROLE.USER,
    endpoint: '/api/log/self/export',
    adminFilters: [],
    selfScope: false,
  },
])(
  'downloads the $endpoint bill with filters allowed for its scope',
  async (testCase) => {
    useAuthStore
      .getState()
      .auth.setUser({ id: 1, username: 'viewer', role: testCase.role })
    const blob = new Blob(['bill'])
    const get = vi.spyOn(api, 'get').mockResolvedValue({ data: blob })
    const createObjectURL = vi.fn(() => 'blob:usage-bill')
    const revokeObjectURL = vi.fn()
    vi.stubGlobal(
      'URL',
      Object.assign(class extends URL {}, { createObjectURL, revokeObjectURL })
    )
    const click = vi
      .spyOn(HTMLAnchorElement.prototype, 'click')
      .mockImplementation(() => undefined)

    renderDownload(
      '/usage-logs/common?model=deepseek-chat&group=premium&username=alice&channel=7&requestId=req-1&startTime=1756684800000&endTime=1759276799000'
    )
    if (testCase.selfScope) {
      await userEvent.click(
        await screen.findByRole('button', { name: 'Only Mine' })
      )
    }
    await userEvent.click(
      await screen.findByRole('button', { name: 'Download usage bill' })
    )

    await waitFor(() => expect(get).toHaveBeenCalledOnce())
    const [requestURL, requestConfig] = get.mock.calls[0]
    expect(requestURL).toContain(`${testCase.endpoint}?`)
    expect(requestURL).toContain('model_name=deepseek-chat')
    expect(requestURL).toContain('group=premium')
    expect(requestURL).toContain('request_id=req-1')
    expect(requestURL).toContain('start_timestamp=1756684800')
    expect(requestURL).toContain('end_timestamp=1759276799')
    for (const filter of testCase.adminFilters) {
      expect(requestURL).toContain(filter)
    }
    if (testCase.role === ROLE.USER || testCase.selfScope) {
      expect(requestURL).not.toContain('username=')
      expect(requestURL).not.toContain('channel=')
    }
    expect(requestConfig).toMatchObject({
      responseType: 'blob',
      skipErrorHandler: true,
    })
    expect(createObjectURL).toHaveBeenCalledWith(blob)
    expect(click).toHaveBeenCalledOnce()
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:usage-bill')
  }
)
