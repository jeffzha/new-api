/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it } from 'vitest'

import { useAuthStore } from '@/stores/auth-store'

import { ProductSwitcher } from '../product-switcher'

afterEach(() => {
  cleanup()
  useAuthStore.getState().auth.reset()
})

describe('ProductSwitcher', () => {
  it('offers the platform and Agency Center to a normal user', async () => {
    useAuthStore.getState().auth.setUser({ id: 1, username: 'user', role: 1 })

    render(<ProductSwitcher />)
    await userEvent.click(screen.getByRole('button', { name: 'Products' }))

    expect(screen.getByRole('menuitem', { name: 'Console' })).toHaveAttribute(
      'aria-disabled',
      'true'
    )
    expect(
      screen.getByRole('menuitem', { name: 'Agency Center' })
    ).toHaveAttribute('href', 'http://localhost:3202/agency/?platform_login=1')
    expect(
      screen.queryByRole('menuitem', { name: 'Model mock scheduling' })
    ).not.toBeInTheDocument()
  })

  it('offers the local overseas Model Mock to administrators', async () => {
    useAuthStore.getState().auth.setUser({ id: 1, username: 'admin', role: 10 })

    render(<ProductSwitcher />)
    await userEvent.click(screen.getByRole('button', { name: 'Products' }))

    expect(
      screen.getByRole('menuitem', { name: 'Model mock scheduling' })
    ).toHaveAttribute('href', 'http://localhost:4173/')
  })
})
