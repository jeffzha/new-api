/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { CreditCard, ShieldCheck, UserRound } from 'lucide-react'
import { afterEach, expect, it } from 'vitest'

import { Tabs, TabsContent, TabsList, TabsTrigger } from '../tabs'

afterEach(cleanup)

it('keeps icon tabs accessible and prevents disabled tab activation during horizontal keyboard navigation', async () => {
  render(
    <Tabs defaultValue='profile'>
      <TabsList aria-label='Account'>
        <TabsTrigger
          value='profile'
          icon={<UserRound aria-label='Decorative profile icon' />}
        >
          Profile
        </TabsTrigger>
        <TabsTrigger value='wallet' icon={<CreditCard />} disabled>
          Wallet
        </TabsTrigger>
        <TabsTrigger value='security' icon={<ShieldCheck />}>
          Security and access preferences
        </TabsTrigger>
      </TabsList>
      <TabsContent value='profile'>Account details</TabsContent>
      <TabsContent value='wallet'>Funding details</TabsContent>
      <TabsContent value='security'>Authentication settings</TabsContent>
    </Tabs>
  )
  const user = userEvent.setup()
  const profile = screen.getByRole('tab', { name: 'Profile' })
  expect(profile).toHaveAttribute('aria-selected', 'true')
  const wallet = screen.getByRole('tab', { name: 'Wallet' })
  expect(wallet).toHaveAttribute('aria-disabled', 'true')
  profile.focus()
  await user.keyboard('{ArrowRight}{Enter}')
  expect(wallet).toHaveAttribute('aria-selected', 'false')
  expect(screen.getByRole('tabpanel', { name: 'Profile' })).toBeVisible()
  await user.keyboard('{ArrowRight}{Enter}')
  const security = screen.getByRole('tab', {
    name: 'Security and access preferences',
  })
  expect(security).toHaveFocus()
  expect(security).toHaveAttribute('aria-selected', 'true')
  expect(
    screen.getByRole('tabpanel', { name: 'Security and access preferences' })
  ).toBeVisible()
  expect(screen.queryByText('Funding details')).not.toBeInTheDocument()
  await user.keyboard('{Home}{Enter}')
  expect(profile).toHaveFocus()
  expect(screen.getByRole('tabpanel', { name: 'Profile' })).toBeVisible()
})

it('keeps the selected panel in sync with controlled values and updated tab labels', async () => {
  function ProviderTabs(props: {
    value: string | null
    label: string
    count: number
  }) {
    return (
      <Tabs value={props.value}>
        <TabsList aria-label='Providers'>
          <TabsTrigger value='all' icon={<UserRound />}>
            All
          </TabsTrigger>
          <TabsTrigger value='provider' icon={<ShieldCheck />}>
            {props.label}
            <span data-slot='tabs-count' aria-hidden>
              {props.count}
            </span>
          </TabsTrigger>
        </TabsList>
        <TabsContent value='all'>All models</TabsContent>
        <TabsContent value='provider'>Provider models</TabsContent>
      </Tabs>
    )
  }

  const { rerender } = render(
    <ProviderTabs value={null} label='Provider' count={0} />
  )
  expect(screen.queryByRole('tab', { selected: true })).not.toBeInTheDocument()
  expect(screen.queryByRole('tabpanel')).not.toBeInTheDocument()

  rerender(<ProviderTabs value='provider' label='Provider' count={9} />)
  expect(
    screen.getByRole('tab', { name: 'Provider', selected: true })
  ).toBeVisible()
  expect(screen.getByRole('tabpanel', { name: 'Provider' })).toHaveTextContent(
    'Provider models'
  )

  rerender(
    <ProviderTabs
      value='provider'
      label='Provider with a longer name'
      count={120}
    />
  )
  const selected = screen.getByRole('tab', {
    name: 'Provider with a longer name',
    selected: true,
  })
  expect(selected).toHaveTextContent('120')
  expect(
    screen.getByRole('tabpanel', { name: 'Provider with a longer name' })
  ).toBeVisible()

  rerender(
    <ProviderTabs value='all' label='Provider with a longer name' count={120} />
  )
  expect(screen.getByRole('tab', { name: 'All', selected: true })).toBeVisible()
  expect(screen.getByRole('tabpanel', { name: 'All' })).toHaveTextContent(
    'All models'
  )
  expect(screen.getAllByRole('tab')).toHaveLength(2)
  const user = userEvent.setup()
  screen.getByRole('tab', { name: 'All' }).focus()
  await user.keyboard('{End}')
  expect(selected).toHaveFocus()
})
