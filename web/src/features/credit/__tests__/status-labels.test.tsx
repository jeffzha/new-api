/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { afterEach, expect, it, vi } from 'vitest'

import zh from '@/i18n/locales/zh.json'
import { api } from '@/lib/api'

import { CreditBook } from '../index'

afterEach(() => vi.restoreAllMocks())

it('shows a suspended contract in Chinese in both its list and selected status', async () => {
  const account = {
    user_id: '42',
    username: 'credit-customer',
    configured: true,
    status: 'suspended',
    credit_limit: 10000,
    used_quota: 2000,
    available_quota: 0,
    payment_term_days: 30,
    repayment_day: 1,
  }
  vi.spyOn(api, 'get').mockImplementation(async (url) => ({
    data: {
      success: true,
      data: url.endsWith('/42')
        ? account
        : { items: url.endsWith('/terms') ? [account] : [] },
    },
  }))
  const locale = createInstance()
  await locale.init({ lng: 'zh', initAsync: false, resources: { zh } })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const view = render(
    <I18nextProvider i18n={locale}>
      <QueryClientProvider client={client}>
        <CreditBook />
      </QueryClientProvider>
    </I18nextProvider>
  )
  try {
    expect(await screen.findByText('已停用')).toBeVisible()
    await userEvent.click(
      screen.getByRole('button', { name: locale.t('Open') })
    )
    expect(await screen.findByRole('combobox')).toHaveTextContent('已停用')
    expect(screen.queryByText('Suspended')).not.toBeInTheDocument()
  } finally {
    view.unmount()
    client.clear()
  }
})
