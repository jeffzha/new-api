/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { act, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18next from 'i18next'
import { afterEach, expect, it, vi } from 'vitest'

import { PricingSidebar } from '../components/pricing-sidebar'

afterEach(async () => {
  await i18next.changeLanguage('en')
})

it('localizes a Chinese supplier name when switching language without changing its filter identity', async () => {
  i18next.addResourceBundle('zh','translation',{'Providers':'供应商'})
  const onVendorChange = vi.fn()
  render(
    <PricingSidebar
      quotaTypeFilter='all'
      endpointTypeFilter='all'
      vendorFilter='字节跳动'
      groupFilter='all'
      tagFilter='all'
      onQuotaTypeChange={vi.fn()}
      onEndpointTypeChange={vi.fn()}
      onVendorChange={onVendorChange}
      onGroupChange={vi.fn()}
      onTagChange={vi.fn()}
      vendors={[{ id: 2, name: '字节跳动' }]}
      groups={['default']}
      tags={[]}
      models={[
        {
          id: 1,
          model_name: 'doubao-test',
          vendor_id: 2,
          vendor_name: '字节跳动',
          quota_type: 0,
          model_ratio: 1,
          completion_ratio: 1,
          enable_groups: ['default'],
        },
      ]}
      hasActiveFilters
      onClearFilters={vi.fn()}
    />
  )
  const provider = screen.getByRole('button', { name: /ByteDance/ })
  expect(provider).toHaveAttribute('aria-pressed', 'true')
  await userEvent.click(provider)
  expect(onVendorChange).toHaveBeenCalledWith('字节跳动')
  await act(async () => {
    await i18next.changeLanguage('zh')
  })
  expect(screen.getByRole('button', { name: /字节跳动/ })).toHaveAttribute(
    'aria-pressed',
    'true'
  )
  await act(async () => {
    await i18next.changeLanguage('en')
  })
  expect(screen.getByRole('button', { name: /ByteDance/ })).toHaveAttribute(
    'aria-pressed',
    'true'
  )
})
