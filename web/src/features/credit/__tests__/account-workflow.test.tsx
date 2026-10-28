/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { CreditBook } from '../index'

const account = {
  user_id: '42',
  username: 'existing-customer',
  configured: true,
  status: 'active',
  credit_limit: 10000,
  used_quota: 0,
  available_quota: 10000,
}
const user = {
  id: 64,
  username: 'new-customer',
  display_name: 'New customer',
  status: 1,
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

async function mount() {
  const locale = createInstance()
  await locale.init({ lng: 'en', initAsync: false, resources: {} })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return render(
    <I18nextProvider i18n={locale}>
      <QueryClientProvider client={client}>
        <CreditBook />
      </QueryClientProvider>
    </I18nextProvider>
  )
}

it('filters configured accounts without resolving an arbitrary user', async () => {
  const get = vi.spyOn(api, 'get').mockImplementation(async (_url, config) => {
    const query = (config?.params as { query?: string } | undefined)?.query
    return {
      data: {
        success: true,
        data: { items: query ? [] : [account], total: query ? 0 : 1 },
      },
    }
  })
  await mount()
  expect(await screen.findByText(account.username)).toBeVisible()
  await userEvent.type(
    screen.getByRole('textbox', { name: 'Search configured credit accounts' }),
    'unconfigured'
  )
  await userEvent.click(screen.getByRole('button', { name: 'Search' }))
  await waitFor(() =>
    expect(screen.queryByText(account.username)).not.toBeInTheDocument()
  )
  expect(get.mock.calls.some(([url]) => url.includes('resolve-user'))).toBe(
    false
  )
  expect(screen.queryByLabelText('Credit limit (CNY)')).not.toBeInTheDocument()
})

it('opens a paginated fuzzy user selector and saves only on explicit submission', async () => {
  const get = vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url.includes('/api/user/search')) {
      const params = new URL(url, 'https://example.test').searchParams
      return {
        data: {
          success: true,
          data: {
            items:
              params.get('p') === '2'
                ? [{ ...user, id: 65, username: 'another-customer' }]
                : [user],
            total: 2,
            page: Number(params.get('p')),
            page_size: 1,
          },
        },
      }
    }
    if (url.endsWith('/64')) {
      return {
        data: {
          success: true,
          data: {
            ...account,
            user_id: '64',
            username: user.username,
            configured: false,
            credit_limit: 0,
          },
        },
      }
    }
    return { data: { success: true, data: { items: [account], total: 1 } } }
  })
  const put = vi.spyOn(api, 'put').mockResolvedValue({
    data: {
      success: true,
      data: { ...account, user_id: '64', username: user.username },
    },
  })
  await mount()
  await userEvent.click(screen.getByRole('button', { name: 'Add credit line' }))
  const dialog = await screen.findByRole('dialog', { name: 'Add credit line' })
  await userEvent.click(
    within(dialog).getByRole('combobox', { name: 'Customer' })
  )
  expect(
    await screen.findByRole('option', { name: /new-customer/ })
  ).toBeVisible()
  fireEvent.scroll(screen.getByRole('listbox'), { target: { scrollTop: 1000 } })
  expect(
    await screen.findByRole('option', { name: /another-customer/ })
  ).toBeVisible()
  await userEvent.type(
    within(dialog).getByRole('combobox', { name: 'Customer' }),
    'new'
  )
  await waitFor(() =>
    expect(
      get.mock.calls.some(
        ([url]) => url.includes('keyword=new') && url.includes('p=1')
      )
    ).toBe(true)
  )
  await userEvent.click(
    await screen.findByRole('option', { name: /new-customer/ })
  )
  await within(dialog).findByLabelText('Credit limit (CNY)')
  expect(put).not.toHaveBeenCalled()
  await userEvent.clear(within(dialog).getByLabelText('Credit limit (CNY)'))
  await userEvent.type(
    within(dialog).getByLabelText('Credit limit (CNY)'),
    '50'
  )
  await userEvent.click(within(dialog).getByRole('button', { name: 'Save' }))
  await waitFor(() => expect(put).toHaveBeenCalledTimes(1))
  await waitFor(() =>
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  )
})

it('does not change credit terms when the add dialog is cancelled', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: { items: [], total: 0 } },
  })
  const put = vi.spyOn(api, 'put')
  await mount()
  await userEvent.click(screen.getByRole('button', { name: 'Add credit line' }))
  await userEvent.click(
    within(await screen.findByRole('dialog')).getByRole('button', {
      name: 'Cancel',
    })
  )
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  expect(put).not.toHaveBeenCalled()
})

function mockSelectableUser(configured = false) {
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url.includes('/api/user/search')) {
      return {
        data: {
          success: true,
          data: { items: [user], total: 1, page: 1, page_size: 20 },
        },
      }
    }
    if (url.endsWith('/64')) {
      return {
        data: {
          success: true,
          data: {
            ...account,
            user_id: '64',
            username: user.username,
            configured,
          },
        },
      }
    }
    return { data: { success: true, data: { items: [account], total: 1 } } }
  })
}

it('selects a user by keyboard and prevents overwriting an existing credit line in the add dialog', async () => {
  mockSelectableUser(true)
  const put = vi.spyOn(api, 'put')
  await mount()
  await userEvent.click(screen.getByRole('button', { name: 'Add credit line' }))
  const dialog = await screen.findByRole('dialog', { name: 'Add credit line' })
  const input = within(dialog).getByRole('combobox', { name: 'Customer' })
  await waitFor(() => expect(input).toHaveFocus())
  expect(input).toHaveAttribute('aria-expanded', 'false')
  await userEvent.keyboard('{ArrowDown}')
  await screen.findByRole('option', { name: /new-customer/ })
  await userEvent.keyboard('{Home}{Enter}')
  expect(
    await within(dialog).findByText(/already has a credit line/)
  ).toBeVisible()
  expect(within(dialog).getByRole('button', { name: 'Save' })).toBeDisabled()
  expect(put).not.toHaveBeenCalled()
})

it('keeps the entered terms visible after a failed save and rejects invalid limits before sending', async () => {
  mockSelectableUser()
  const put = vi.spyOn(api, 'put').mockResolvedValue({
    data: { success: false, message: 'Credit update rejected' },
  })
  await mount()
  await userEvent.click(screen.getByRole('button', { name: 'Add credit line' }))
  const dialog = await screen.findByRole('dialog', { name: 'Add credit line' })
  await userEvent.click(
    within(dialog).getByRole('combobox', { name: 'Customer' })
  )
  await userEvent.click(
    await screen.findByRole('option', { name: /new-customer/ })
  )
  const limit = await within(dialog).findByLabelText('Credit limit (CNY)')
  await userEvent.clear(limit)
  await userEvent.type(limit, '-1')
  await userEvent.click(within(dialog).getByRole('button', { name: 'Save' }))
  expect(await within(dialog).findByRole('alert')).toBeVisible()
  expect(limit).toHaveAttribute('aria-invalid', 'true')
  expect(put).not.toHaveBeenCalled()
  await userEvent.clear(limit)
  await userEvent.type(limit, '50')
  await userEvent.click(within(dialog).getByRole('button', { name: 'Save' }))
  expect(
    await within(dialog).findByText('Failed to save credit terms')
  ).toBeVisible()
  expect(limit).toHaveValue('50')
  expect(dialog).toBeVisible()
  expect(put).toHaveBeenCalledTimes(1)
})

it('opens collection and bad-debt dialogs from an existing account without changing any balance', async () => {
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url.includes('/statements')) {
      return {
        data: {
          success: true,
          data: {
            items: [
              {
                id: 3,
                user_id: '42',
                period: '2026-09',
                status: 'overdue',
                remaining_quota: 10000,
                due_at_ms: 1,
              },
            ],
          },
        },
      }
    }
    return {
      data: {
        success: true,
        data: url.endsWith('/42') ? account : { items: [account], total: 1 },
      },
    }
  })
  const post = vi.spyOn(api, 'post')
  await mount()
  await userEvent.click(await screen.findByRole('button', { name: 'Open' }))
  await userEvent.click(await screen.findByRole('button', { name: 'Collect' }))
  expect(
    await screen.findByRole('dialog', { name: 'Register collection' })
  ).toBeVisible()
  await userEvent.keyboard('{Escape}')
  await waitFor(() =>
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  )
  await userEvent.click(screen.getByRole('button', { name: /^Write off$/ }))
  expect(
    await screen.findByRole('dialog', { name: 'Write off bad debt' })
  ).toBeVisible()
  expect(post).not.toHaveBeenCalled()
})
