/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { cleanup, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it } from 'vitest'

import { ConfigDrawer } from '@/components/config-drawer'
import { SidebarProvider } from '@/components/ui/sidebar'
import { DirectionProvider } from '@/context/direction-provider'
import { LayoutProvider } from '@/context/layout-provider'
import { ThemeCustomizationProvider } from '@/context/theme-customization-provider'
import { ThemeProvider } from '@/context/theme-provider'
import { getCookie } from '@/lib/cookies'

function mount() {
  return render(
    <ThemeProvider>
      <ThemeCustomizationProvider>
        <DirectionProvider>
          <LayoutProvider>
            <SidebarProvider>
              <ConfigDrawer />
            </SidebarProvider>
          </LayoutProvider>
        </DirectionProvider>
      </ThemeCustomizationProvider>
    </ThemeProvider>
  )
}

afterEach(() => {
  cleanup()
  for (const cookie of document.cookie.split(';')) {
    document.cookie = `${cookie.split('=')[0].trim()}=; max-age=0; path=/`
  }
  document.body.removeAttribute('data-theme-preset')
  document.documentElement.classList.remove('dark', 'light')
})

describe('Prism theme selection', () => {
  it('adds a persisted color choice without overriding the selected light mode', async () => {
    document.cookie = 'theme_preset=signal-console; path=/'
    document.cookie = 'vite-ui-theme=light; path=/'
    const view = mount()
    const user = userEvent.setup()
    await user.click(
      screen.getByRole('button', { name: 'Open theme settings' })
    )
    const presets = screen.getByRole('radiogroup', {
      name: 'Select color preset',
    })
    await user.click(
      within(presets).getByRole('radio', { name: 'preset.prism-console' })
    )
    expect(document.body).toHaveAttribute('data-theme-preset', 'prism-console')
    expect(getCookie('theme_preset')).toBeUndefined()
    expect(getCookie('vite-ui-theme')).toBe('light')
    expect(document.documentElement).toHaveClass('light')
    view.unmount()
    mount()
    expect(document.body).toHaveAttribute('data-theme-preset', 'prism-console')
    expect(document.documentElement).toHaveClass('light')
  })

  it('allows keyboard selection back to Signal without changing dark mode', async () => {
    document.cookie = 'theme_preset=prism-console; path=/'
    document.cookie = 'vite-ui-theme=dark; path=/'
    mount()
    const user = userEvent.setup()
    await user.click(
      screen.getByRole('button', { name: 'Open theme settings' })
    )
    screen.getByRole('radio', { name: 'preset.prism-console' }).focus()
    await user.keyboard('{ArrowRight}')
    expect(
      screen.getByRole('radio', { name: 'preset.signal-console' })
    ).toBeChecked()
    expect(document.body).toHaveAttribute('data-theme-preset', 'signal-console')
    expect(document.documentElement).toHaveClass('dark')
    expect(getCookie('vite-ui-theme')).toBe('dark')
  })

  it('uses Prism and light mode for fresh browsers and restores them on reset', async () => {
    mount()
    expect(document.body).toHaveAttribute('data-theme-preset', 'prism-console')
    expect(document.documentElement).toHaveClass('light')
    const user = userEvent.setup()
    await user.click(
      screen.getByRole('button', { name: 'Open theme settings' })
    )
    expect(screen.getByRole('radio', { name: 'Select light' })).toBeChecked()
    await user.click(screen.getByRole('radio', { name: 'preset.default' }))
    await user.click(screen.getByRole('radio', { name: 'Select dark' }))
    await user.click(
      screen.getByRole('button', {
        name: 'Reset all settings to default values',
      })
    )
    expect(document.body).toHaveAttribute('data-theme-preset', 'prism-console')
    expect(getCookie('theme_preset')).toBeUndefined()
    expect(screen.getByRole('radio', { name: 'Select light' })).toBeChecked()
    expect(document.documentElement).toHaveClass('light')
  })

  it('keeps previously saved preset and color mode preferences', () => {
    document.cookie = 'theme_preset=default; path=/'
    document.cookie = 'vite-ui-theme=dark; path=/'
    mount()
    expect(document.body).toHaveAttribute('data-theme-preset', 'default')
    expect(document.documentElement).toHaveClass('dark')
  })
})
