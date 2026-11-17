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

function renderCard(
  walletBalances: UserWalletData['wallet_balances'],
  credit?: UserWalletData['credit']
) {
  const user: UserWalletData = {
    id: 1,
    username: 'wallet-user',
    quota: 1000,
    wallet_balances: walletBalances,
    credit,
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

it('shows an unconfigured credit account with zero amounts rather than hiding it', () => {
  renderCard([], {
    configured: false,
    status: 'suspended',
    credit_limit: 0,
    used_quota: 0,
    available_quota: 0,
    repayment_day: 1,
    payment_term_days: 30,
    next_statement_at_ms: 0,
  })
  const credit = screen.getByLabelText('Credit account')
  expect(within(credit).getByText('Credit limit')).toBeVisible()
  expect(within(credit).getByText('Available credit')).toBeVisible()
  expect(within(credit).getByText('Credit not configured')).toBeVisible()
})

it('shows approved and used credit separately from wallet money for an active contract', () => {
  renderCard([{ type: 'recharge', quota: 1000 }], {
    configured: true,
    status: 'active',
    credit_limit: 10000,
    used_quota: 2000,
    available_quota: 8000,
    repayment_day: 1,
    payment_term_days: 30,
    next_statement_at_ms: 0,
  })
  const credit = screen.getByLabelText('Credit account')
  expect(within(credit).getByText('Used credit')).toBeVisible()
  expect(within(credit).getByText('Available credit')).toBeVisible()
  const breakdown = screen.getByRole('group', { name: 'Balance breakdown' })
  expect(within(breakdown).queryByText('Credit limit')).not.toBeInTheDocument()
})

it('keeps the debt visible when a configured credit line is suspended', () => {
  renderCard([], {
    configured: true,
    status: 'suspended',
    credit_limit: 10000,
    used_quota: 2000,
    available_quota: 0,
    repayment_day: 1,
    payment_term_days: 30,
    next_statement_at_ms: 0,
  })
  expect(screen.getByText('Credit line suspended')).toBeVisible()
  expect(
    within(screen.getByLabelText('Credit account')).getByText('Used credit')
  ).toBeVisible()
})
