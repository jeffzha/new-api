/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { cleanup, render, screen, within } from '@testing-library/react'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { afterEach, expect, it } from 'vitest'

import type { UserWalletData } from '../../types'
import { WalletStatsCard } from '../wallet-stats-card'

const i18n = createInstance()
await i18n.init({
  lng: 'en',
  initAsync: false,
  resources: {
    en: {
      translation: {
        'Current Balance': 'Current Balance',
        'Recharge balance': 'Recharge balance',
        'Gift balance': 'Gift balance',
        'Redemption code balance': 'Redemption code balance',
        'Other balance': 'Other balance',
        'Balance breakdown': 'Balance breakdown',
        'Balance category': '{{type}} balance',
      },
    },
  },
})

afterEach(cleanup)

function renderCard(walletBalances: UserWalletData['wallet_balances']) {
  const user: UserWalletData = {
    id: 1,
    username: 'wallet-user',
    quota: 1000,
    wallet_balances: walletBalances,
    used_quota: 200,
    request_count: 3,
    aff_quota: 0,
    aff_history_quota: 0,
    aff_count: 0,
    group: 'default',
  }
  return render(
    <I18nextProvider i18n={i18n}>
      <WalletStatsCard user={user} />
    </I18nextProvider>
  )
}

it('shows recharge, gift, redemption and reconciled other balances', () => {
  renderCard([
    { type: 'recharge', quota: 400 },
    { type: 'gift', quota: 300 },
    { type: 'redemption', quota: 200 },
    { type: 'other', quota: 100 },
  ])

  const breakdown = screen.getByRole('group', {
    name: 'Balance breakdown',
  })
  expect(within(breakdown).getByText('Recharge balance')).toBeVisible()
  expect(within(breakdown).getByText('Gift balance')).toBeVisible()
  expect(within(breakdown).getByText('Redemption code balance')).toBeVisible()
  expect(within(breakdown).getByText('Other balance')).toBeVisible()
})

it('renders future balance types without changing the component contract', () => {
  renderCard([{ type: 'subscription_credit', quota: 1000 }])

  expect(screen.getByText('subscription_credit balance')).toBeVisible()
})

it('keeps the current balance card full width on narrow layouts', () => {
  renderCard([])

  expect(
    screen.getByText('Current Balance').closest('.col-span-2')
  ).not.toBeNull()
  expect(
    screen.queryByRole('group', { name: 'Balance breakdown' })
  ).not.toBeInTheDocument()
})

it('uses the same narrow layout while balance data is loading', () => {
  render(
    <I18nextProvider i18n={i18n}>
      <WalletStatsCard user={null} loading />
    </I18nextProvider>
  )

  const loadingItems = screen.getAllByLabelText('Loading...')
  expect(loadingItems).toHaveLength(3)
  expect(loadingItems[0]).toHaveClass('col-span-2', 'sm:col-span-1')
})
