/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it } from 'vitest'

import {
  ThemeCustomizationProvider,
  useThemeCustomization,
} from '@/context/theme-customization-provider'

import { PageFooterPortal } from '../page-footer'
import { SectionPageLayout } from '../section-page-layout'

function ThemeToggle() {
  const { setPreset } = useThemeCustomization()
  return (
    <button type='button' onClick={() => setPreset('default')}>
      Restore default
    </button>
  )
}

afterEach(() => {
  cleanup()
  document.cookie = 'theme_preset=; max-age=0; path=/'
  document.body.removeAttribute('data-theme-preset')
})

describe('Signal page composition', () => {
  it('switching to Default removes Signal context without losing page actions or content', async () => {
    document.cookie = 'theme_preset=signal-console; path=/'
    render(
      <ThemeCustomizationProvider>
        <ThemeToggle />
        <SectionPageLayout fixedContent>
          <SectionPageLayout.Title>API credentials</SectionPageLayout.Title>
          <SectionPageLayout.Actions>
            <button type='button'>Create key</button>
          </SectionPageLayout.Actions>
          <SectionPageLayout.Content>
            <label>
              Filter
              <input />
            </label>
            <p>No credentials</p>
          </SectionPageLayout.Content>
        </SectionPageLayout>
      </ThemeCustomizationProvider>
    )
    expect(screen.getByRole('heading', { name: 'API credentials' })).toBeInTheDocument()
    await userEvent.click(
      screen.getByRole('button', { name: 'Restore default' })
    )
    expect(screen.queryByText('02 / API ACCESS')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Create key' })).toBeEnabled()
    expect(
      screen.getByRole('heading', { name: 'API credentials' })
    ).toBeInTheDocument()
    expect(screen.getByLabelText('Filter')).toBeInTheDocument()
    expect(document.body).toHaveAttribute('data-theme-preset', 'default')
  })
  it('fixed data pages retain bounded content and persistent pagination', () => {
    document.cookie = 'theme_preset=signal-console; path=/'
    render(
      <ThemeCustomizationProvider>
        <SectionPageLayout fixedContent>
          <SectionPageLayout.Title>Audit</SectionPageLayout.Title>
          <SectionPageLayout.Content>
            <p>No records</p>
            <PageFooterPortal>
              <button type='button'>Next page</button>
            </PageFooterPortal>
          </SectionPageLayout.Content>
        </SectionPageLayout>
      </ThemeCustomizationProvider>
    )
    expect(screen.getByText('No records').parentElement).toHaveClass(
      'overflow-hidden',
      'min-h-0'
    )
    expect(
      screen
        .getByRole('button', { name: 'Next page' })
        .closest('.signal-console-page-content')
    ).toBeNull()
    expect(
      screen
        .getByRole('button', { name: 'Next page' })
        .closest('.signal-console-page-footer')
    ).toHaveClass('shrink-0')
  })
  it('settings pages keep long content scrollable', () => {
    render(
      <SectionPageLayout>
        <SectionPageLayout.Content>
          <p>Account settings</p>
        </SectionPageLayout.Content>
      </SectionPageLayout>
    )
    expect(screen.getByText('Account settings').parentElement).toHaveClass(
      'overflow-auto',
      'min-h-0'
    )
  })
})
