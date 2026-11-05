import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
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
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { toast } from 'sonner'
import { afterEach, expect, test, vi } from 'vitest'

import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { api } from '@/lib/api'

import { UpstreamSyncMenuItem } from '../upstream-sync-menu-item'

afterEach(() => vi.restoreAllMocks())

function menu(disabled = false) {
  const client = new QueryClient({
    defaultOptions: { mutations: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <DropdownMenu>
        <DropdownMenuTrigger render={<Button>More</Button>} />
        <DropdownMenuContent>
          <UpstreamSyncMenuItem disabled={disabled} />
        </DropdownMenuContent>
      </DropdownMenu>
    </QueryClientProvider>
  )
  return userEvent.setup()
}

test('keyboard submission stays pending until queued and confirms background processing', async () => {
  let resolve!: (value: unknown) => void
  const post = vi.spyOn(api, 'post').mockImplementation(
    () =>
      new Promise((done) => {
        resolve = done
      })
  )
  const success = vi.spyOn(toast, 'success').mockImplementation(() => 'toast')
  const user = menu()
  await user.tab()
  await user.keyboard('{ArrowDown}')
  const item = await screen.findByRole('menuitem', {
    name: 'Register upstreams with OPS',
  })
  await user.keyboard('{Enter}')
  await waitFor(() => expect(item).toHaveAttribute('aria-busy', 'true'))
  expect(item).toHaveAttribute('aria-disabled', 'true')
  expect(post).toHaveBeenCalledWith('/api/channel/upstreams/sync')
  await act(async () =>
    resolve({ data: { success: true, data: { status: 'queued' } } })
  )
  await waitFor(() => expect(success).toHaveBeenCalled())
  expect(item).toHaveAttribute('aria-busy', 'false')
})

test('operator without permission cannot submit registration', async () => {
  const post = vi.spyOn(api, 'post')
  const user = menu(true)
  await user.click(screen.getByRole('button', { name: 'More' }))
  const item = await screen.findByRole('menuitem', {
    name: 'Register upstreams with OPS',
  })
  expect(item).toHaveAttribute('aria-disabled', 'true')
  await user.click(item)
  expect(post).not.toHaveBeenCalled()
})

test('business rejection never confirms registration and allows another attempt', async () => {
  vi.spyOn(api, 'post').mockResolvedValue({
    data: { success: false, message: 'Unavailable' },
  })
  const success = vi.spyOn(toast, 'success')
  const user = menu()
  await user.click(screen.getByRole('button', { name: 'More' }))
  const item = await screen.findByRole('menuitem', {
    name: 'Register upstreams with OPS',
  })
  await user.click(item)
  await waitFor(() => expect(item).toHaveAttribute('aria-busy', 'false'))
  expect(success).not.toHaveBeenCalled()
  expect(item).not.toHaveAttribute('aria-disabled', 'true')
})
