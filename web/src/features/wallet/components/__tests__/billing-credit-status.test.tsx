/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { render, screen } from '@testing-library/react'
import { afterEach, expect, it } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import type { TopupRecord } from '../../types'
import { BillingHistoryDialog } from '../dialogs/billing-history-dialog'

const originalAdapter = api.defaults.adapter

afterEach(() => {
  api.defaults.adapter = originalAdapter
  useAuthStore.getState().auth.setUser(null)
})

it.each([
  ['queued', 'Wallet credit queued'],
  ['accepted', 'Credit submitted; balance confirmation pending'],
  ['unconfirmed', 'Wallet credit unconfirmed'],
] as const)(
  'keeps a successful order distinct from %s wallet credit',
  async (status, label) => {
    useAuthStore.getState().auth.setUser({
      id: 1,
      username: 'funding-user',
      role: 1,
    })
    const record: TopupRecord = {
      id: 10,
      user_id: 1,
      amount: 10,
      money: 10,
      trade_no: 'funding-order',
      payment_method: 'alipay',
      create_time: 1795564800,
      status: 'success',
      wallet_credit: { source: 'ops', status },
    }
    api.defaults.adapter = async (config) => ({
      config,
      status: 200,
      statusText: 'OK',
      headers: {},
      data: { success: true, data: { items: [record], total: 1 } },
    })
    render(<BillingHistoryDialog open onOpenChange={() => undefined} />)
    expect(await screen.findByText(label)).toBeVisible()
    expect(screen.getByText('Order processed')).toBeVisible()
    expect(screen.queryByText('Success')).not.toBeInTheDocument()
  }
)

it('retains the successful order display for historical records without OPS evidence', async () => {
  api.defaults.adapter = async (config) => ({
    config,
    status: 200,
    statusText: 'OK',
    headers: {},
    data: {
      success: true,
      data: {
        items: [{
          id: 11,
          user_id: 1,
          amount: 10,
          money: 10,
          trade_no: 'legacy-order',
          payment_method: 'alipay',
          create_time: 1795564800,
          status: 'success',
        }],
        total: 1,
      },
    },
  })
  render(<BillingHistoryDialog open onOpenChange={() => undefined} />)
  expect(await screen.findByText('Success')).toBeVisible()
  expect(screen.queryByText('Order processed')).not.toBeInTheDocument()
})
