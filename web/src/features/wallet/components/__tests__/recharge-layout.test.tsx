/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { cleanup, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ComponentProps } from 'react'
import { afterEach, expect, it, vi } from 'vitest'

import type { TopupInfo } from '../../types'
import { RechargeFormCard } from '../recharge-form-card'

const topupInfo: TopupInfo = {
  enable_online_topup: true,
  enable_stripe_topup: false,
  min_topup: 10,
  stripe_min_topup: 10,
  amount_options: [10, 100],
  discount: {},
  pay_methods: [{ type: 'alipay', name: 'Alipay' }],
}

const props: ComponentProps<typeof RechargeFormCard> = {
  workspace: true,
  topupInfo,
  presetAmounts: [{ value: 10 }, { value: 100 }],
  selectedPreset: 10,
  topupAmount: 10,
  paymentAmount: 10,
  calculating: false,
  paymentLoading: null,
  redemptionCode: '',
  redeeming: false,
  onSelectPreset: vi.fn(),
  onTopupAmountChange: vi.fn(),
  onPaymentMethodSelect: vi.fn(),
  onRedemptionCodeChange: vi.fn(),
  onRedeem: vi.fn(),
}

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

it('separates amount selection from the payment rail and retains the payment callback', async () => {
  const { rerender } = render(<RechargeFormCard {...props} />)
  const amounts = screen.getByRole('group', { name: 'Amount' })
  const selected = within(amounts).getByRole('button', { pressed: true })
  expect(selected).toHaveAccessibleName(/10/)
  expect(selected.querySelector('svg[aria-hidden="true"]')).toBeInTheDocument()
  await userEvent.click(within(amounts).getByRole('button', { pressed: false }))
  expect(props.onSelectPreset).toHaveBeenCalledWith({ value: 100 })
  rerender(
    <RechargeFormCard {...props} selectedPreset={100} topupAmount={100} />
  )
  expect(
    within(amounts).getByRole('button', { pressed: true })
  ).toHaveAccessibleName(/100/)
  const payment = screen.getByRole('region', { name: 'Payment Method' })
  await userEvent.click(within(payment).getByRole('button', { name: 'Alipay' }))
  expect(props.onPaymentMethodSelect).toHaveBeenCalledWith({
    type: 'alipay',
    name: 'Alipay',
  })
})

it('keeps payment minimums and redemption restrictions in the new layout', () => {
  render(
    <RechargeFormCard
      {...props}
      topupAmount={5}
      topupInfo={{ ...topupInfo, enable_redemption: false }}
    />
  )
  expect(screen.getByRole('button', { name: /Alipay/ })).toBeDisabled()
  expect(
    screen.getByText(
      'Redemption codes are disabled until the administrator confirms compliance terms.'
    )
  ).toBeVisible()
  expect(
    screen.queryByRole('textbox', { name: 'Have a Code?' })
  ).not.toBeInTheDocument()
})

it('shows the payment-unavailable state without an empty checkout rail', () => {
  render(<RechargeFormCard {...props} topupInfo={null} />)
  expect(
    screen.getByText(
      'Online topup is not enabled. Please use redemption code or contact administrator.'
    )
  ).toBeVisible()
  expect(
    screen.queryByRole('region', { name: 'Payment Method' })
  ).not.toBeInTheDocument()
  expect(screen.getByRole('textbox', { name: 'Have a Code?' })).toBeVisible()
})
