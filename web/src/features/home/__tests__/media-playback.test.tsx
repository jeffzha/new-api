/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { ShowcaseVideo } from '../components/showcase-video'

let intersect: IntersectionObserverCallback
let reduced = false
let visible = true
let hidden = false
let changeMotion: () => void

beforeEach(() => {
  reduced = false
  visible = true
  hidden = false
  vi.spyOn(document, 'hidden', 'get').mockImplementation(() => hidden)
  vi.spyOn(window, 'matchMedia').mockImplementation(
    () =>
      ({
        get matches() {
          return reduced
        },
        addEventListener: (_: string, callback: () => void) => {
          changeMotion = callback
        },
        removeEventListener: vi.fn(),
      }) as unknown as MediaQueryList
  )
  vi.stubGlobal(
    'IntersectionObserver',
    class {
      constructor(callback: IntersectionObserverCallback) {
        intersect = callback
      }
      observe() {
        intersect(
          [{ isIntersecting: visible } as IntersectionObserverEntry],
          this as unknown as IntersectionObserver
        )
      }
      disconnect() {}
    }
  )
  vi.spyOn(HTMLMediaElement.prototype, 'play').mockImplementation(
    function (this: HTMLMediaElement) {
      this.dispatchEvent(new Event('play'))
      return Promise.resolve()
    }
  )
  vi.spyOn(HTMLMediaElement.prototype, 'pause').mockImplementation(
    function (this: HTMLMediaElement) {
      this.dispatchEvent(new Event('pause'))
    }
  )
})

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

function mount() {
  return render(
    <ShowcaseVideo
      src='/preview.mp4'
      poster='/poster.webp'
      label='Creative preview'
    />
  )
}

describe('Homepage media playback', () => {
  it('autoplays muted inline video when visible and supports keyboard pause and resume', async () => {
    const user = userEvent.setup()
    mount()
    const media = screen.getByLabelText('Creative preview') as HTMLVideoElement
    expect(media.muted).toBe(true)
    expect(media).toHaveAttribute('playsinline')
    expect(media).toHaveAttribute('loop')
    const pause = await screen.findByRole('button', {
      name: 'Pause preview: Creative preview',
    })
    pause.focus()
    await user.keyboard('{Enter}')
    expect(
      screen.getByRole('button', { name: 'Play preview: Creative preview' })
    ).toBeVisible()
    await user.keyboard('{Enter}')
    expect(
      screen.getByRole('button', { name: 'Pause preview: Creative preview' })
    ).toBeVisible()
  })

  it('stops when offscreen or the document is hidden and resumes on return', async () => {
    mount()
    await screen.findByRole('button', { name: /Pause preview/ })
    act(() => {
      visible = false
      intersect(
        [{ isIntersecting: false } as IntersectionObserverEntry],
        {} as IntersectionObserver
      )
    })
    expect(screen.getByRole('button', { name: /Play preview/ })).toBeVisible()
    act(() => {
      visible = true
      intersect(
        [{ isIntersecting: true } as IntersectionObserverEntry],
        {} as IntersectionObserver
      )
    })
    expect(screen.getByRole('button', { name: /Pause preview/ })).toBeVisible()
    act(() => {
      hidden = true
      document.dispatchEvent(new Event('visibilitychange'))
    })
    expect(screen.getByRole('button', { name: /Play preview/ })).toBeVisible()
    act(() => {
      hidden = false
      document.dispatchEvent(new Event('visibilitychange'))
    })
    expect(screen.getByRole('button', { name: /Pause preview/ })).toBeVisible()
  })

  it('honors reduced motion, while allowing an explicit play request', async () => {
    reduced = true
    const user = userEvent.setup()
    mount()
    expect(HTMLMediaElement.prototype.play).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: /Play preview/ }))
    expect(screen.getByRole('button', { name: /Pause preview/ })).toBeVisible()
  })

  it('keeps the preview usable when intersection observation is unavailable', async () => {
    vi.stubGlobal('IntersectionObserver', undefined)
    mount()
    expect(
      await screen.findByRole('button', { name: /Pause preview/ })
    ).toBeVisible()
  })

  it('stops automatic playback when reduced motion is enabled after mount', async () => {
    mount()
    await screen.findByRole('button', { name: /Pause preview/ })
    act(() => {
      reduced = true
      changeMotion()
    })
    expect(screen.getByRole('button', { name: /Play preview/ })).toBeVisible()
  })

  it('does not resume a user-paused preview after returning to the viewport', async () => {
    const user = userEvent.setup()
    mount()
    await user.click(
      await screen.findByRole('button', { name: /Pause preview/ })
    )
    act(() =>
      intersect(
        [{ isIntersecting: false } as IntersectionObserverEntry],
        {} as IntersectionObserver
      )
    )
    act(() =>
      intersect(
        [{ isIntersecting: true } as IntersectionObserverEntry],
        {} as IntersectionObserver
      )
    )
    expect(screen.getByRole('button', { name: /Play preview/ })).toBeVisible()
  })

  it('keeps the poster and play control when autoplay is blocked', async () => {
    vi.mocked(HTMLMediaElement.prototype.play).mockRejectedValue(
      new Error('Autoplay blocked')
    )
    mount()
    await waitFor(() =>
      expect(screen.getByRole('button', { name: /Play preview/ })).toBeVisible()
    )
    expect(screen.getByLabelText('Creative preview')).toHaveAttribute(
      'poster',
      '/poster.webp'
    )
  })

  it('shows an accessible poster fallback if the media fails', () => {
    mount()
    fireEvent.error(screen.getByLabelText('Creative preview'))
    expect(
      screen.getByRole('img', { name: 'Creative preview' })
    ).toHaveAttribute('src', '/poster.webp')
    expect(
      screen.getByText('Video unavailable. Showing preview image.')
    ).toBeVisible()
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
  })
})
