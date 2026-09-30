/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { act, cleanup, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { RoutingScene } from '../components/routing-scene'

let reducedMotion = false
let motionPreference: MediaQueryList

beforeEach(() => {
  reducedMotion = false
  const events = new EventTarget()
  motionPreference = {
    get matches() {
      return reducedMotion
    },
    media: '(prefers-reduced-motion: reduce)',
    onchange: null,
    addEventListener: events.addEventListener.bind(events),
    removeEventListener: events.removeEventListener.bind(events),
    dispatchEvent: events.dispatchEvent.bind(events),
    addListener: vi.fn(),
    removeListener: vi.fn(),
  }
  vi.spyOn(window, 'matchMedia').mockReturnValue(motionPreference)
  vi.spyOn(window, 'requestAnimationFrame').mockReturnValue(1)
  vi.spyOn(window, 'cancelAnimationFrame').mockImplementation(() => undefined)
  vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
  vi.stubGlobal(
    'IntersectionObserver',
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
  )
  const context = {
    clearRect: vi.fn(),
    beginPath: vi.fn(),
    moveTo: vi.fn(),
    lineTo: vi.fn(),
    stroke: vi.fn(),
    bezierCurveTo: vi.fn(),
    fillRect: vi.fn(),
    setTransform: vi.fn(),
  } as unknown as CanvasRenderingContext2D
  vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(context)
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('Homepage routing illustration', () => {
  it('connects the edges of the gateway nodes without drawing through their labels', () => {
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(
      function (this: HTMLElement) {
        let bounds = { x: 0, y: 0, width: 600, height: 400 }
        if (this.classList.contains('routing-source')) {
          bounds = { x: 0, y: 166, width: 64, height: 68 }
        } else if (
          this.classList.contains('routing-hub') ||
          this.classList.contains('routing-hub-chip')
        ) {
          bounds = { x: 148, y: 126, width: 156, height: 148 }
        } else if (this.classList.contains('routing-provider')) {
          bounds = { x: 500, y: 60, width: 100, height: 48 }
        }
        return DOMRect.fromRect(bounds)
      }
    )
    const view = render(<RoutingScene systemName='NEXIGHT' />)
    const context = view.container.querySelector('canvas')?.getContext('2d')

    expect(context?.bezierCurveTo).toHaveBeenNthCalledWith(
      1,
      106,
      200,
      106,
      200,
      148,
      200
    )
    expect(context?.bezierCurveTo).toHaveBeenNthCalledWith(
      2,
      402,
      200,
      402,
      84,
      500,
      84
    )
  })

  it('selects only the clicked route and supports keyboard selection', async () => {
    const user = userEvent.setup()
    render(<RoutingScene systemName='NEXIGHT' />)
    const routes = within(
      screen.getByRole('group', { name: 'Model routing illustration' })
    )
    const deepseek = routes.getByRole('button', {
      name: 'Select DeepSeek route',
    })
    const openai = routes.getByRole('button', { name: 'Select OpenAI route' })
    const claude = routes.getByRole('button', { name: 'Select Claude route' })
    expect(deepseek).toHaveAttribute('aria-pressed', 'true')

    await user.click(openai)
    expect(openai).toHaveAttribute('aria-pressed', 'true')
    expect(deepseek).toHaveAttribute('aria-pressed', 'false')

    await user.tab()
    expect(claude).toHaveFocus()
    await user.keyboard('{Enter}')
    expect(claude).toHaveAttribute('aria-pressed', 'true')
    expect(openai).toHaveAttribute('aria-pressed', 'false')
    expect(routes.getAllByRole('button', { pressed: true })).toHaveLength(1)
  })

  it('preserves the paused animation while changing routes and can resume by keyboard', async () => {
    const user = userEvent.setup()
    render(<RoutingScene systemName='NEXIGHT' />)
    const pause = screen.getByRole('button', {
      name: 'Pause routing animation',
    })
    await user.click(pause)
    const resume = screen.getByRole('button', {
      name: 'Resume routing animation',
    })
    expect(resume).toHaveAttribute('aria-pressed', 'true')
    vi.mocked(window.requestAnimationFrame).mockClear()

    await user.click(
      screen.getByRole('button', { name: 'Select Gemini route' })
    )
    expect(resume).toHaveAttribute('aria-pressed', 'true')
    expect(window.requestAnimationFrame).not.toHaveBeenCalled()

    resume.focus()
    await user.keyboard('{Enter}')
    expect(
      screen.getByRole('button', { name: 'Pause routing animation' })
    ).toHaveAttribute('aria-pressed', 'false')
    expect(window.requestAnimationFrame).toHaveBeenCalled()
  })

  it('does not animate when reduced motion is requested and responds to preference changes', () => {
    reducedMotion = true
    render(<RoutingScene systemName='NEXIGHT' />)
    expect(window.requestAnimationFrame).not.toHaveBeenCalled()
    expect(
      screen.getByRole('button', { name: 'Select DeepSeek route' })
    ).toHaveAttribute('aria-pressed', 'true')

    act(() => {
      reducedMotion = false
      motionPreference.dispatchEvent(new Event('change'))
    })
    expect(window.requestAnimationFrame).toHaveBeenCalled()
    vi.mocked(window.requestAnimationFrame).mockClear()
    vi.mocked(window.cancelAnimationFrame).mockClear()

    act(() => {
      reducedMotion = true
      motionPreference.dispatchEvent(new Event('change'))
    })
    expect(window.cancelAnimationFrame).toHaveBeenCalled()
    expect(window.requestAnimationFrame).not.toHaveBeenCalled()
  })

  it('keeps provider controls usable when canvas rendering is unavailable', async () => {
    vi.mocked(HTMLCanvasElement.prototype.getContext).mockReturnValue(null)
    const user = userEvent.setup()
    render(<RoutingScene systemName='NEXIGHT' />)
    expect(screen.getByText('NEXIGHT')).toBeVisible()
    await user.click(
      screen.getByRole('button', { name: 'Select OpenAI route' })
    )
    expect(
      screen.getByRole('button', { name: 'Select OpenAI route' })
    ).toHaveAttribute('aria-pressed', 'true')
    expect(
      screen.getByRole('button', { name: 'Select DeepSeek route' })
    ).toHaveAttribute('aria-pressed', 'false')
  })
})
