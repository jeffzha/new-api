/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it } from 'vitest'

import { Input } from '@/components/ui/input'

import { WalletOperations } from '../wallet-operations'

afterEach(cleanup)

it('switches funding and subscription panels by keyboard without losing the entered amount', async () => {
  render(
    <WalletOperations
      workspace
      hasSubscriptions
      funds={<Input aria-label='Amount' defaultValue='10' />}
      subscriptions={<p>Current plan</p>}
    />
  )
  const user = userEvent.setup()
  await user.clear(screen.getByRole('textbox', { name: 'Amount' }))
  await user.type(screen.getByRole('textbox', { name: 'Amount' }), '75')
  screen.getByRole('tab', { name: 'Add Funds' }).focus()
  await user.keyboard('{ArrowRight}{Enter}')
  expect(screen.getByRole('tab', { name: 'Subscriptions' })).toHaveAttribute(
    'aria-selected',
    'true'
  )
  expect(screen.getByRole('tabpanel', { name: 'Subscriptions' })).toBeVisible()
  expect(
    screen.queryByRole('textbox', { name: 'Amount' })
  ).not.toBeInTheDocument()
  await user.click(screen.getByRole('tab', { name: 'Add Funds' }))
  expect(screen.getByRole('textbox', { name: 'Amount' })).toHaveValue('75')
})

it('shows a clear empty state when subscriptions are unavailable', async () => {
  render(
    <WalletOperations
      workspace
      hasSubscriptions={false}
      funds={<p>Funding</p>}
      subscriptions={null}
    />
  )
  await userEvent.click(screen.getByRole('tab', { name: 'Subscriptions' }))
  expect(screen.getByText('No subscription plans available')).toBeVisible()
  expect(
    screen.queryByRole('tabpanel', { name: 'Add Funds' })
  ).not.toBeInTheDocument()
})

it('retains the simultaneous funding and subscription layout for the default theme', () => {
  render(
    <WalletOperations
      workspace={false}
      hasSubscriptions
      funds={<p>Funding</p>}
      subscriptions={<p>Current plan</p>}
    />
  )
  expect(screen.getByText('Funding')).toBeVisible()
  expect(screen.getByText('Current plan')).toBeVisible()
  expect(screen.queryByRole('tablist')).not.toBeInTheDocument()
})
