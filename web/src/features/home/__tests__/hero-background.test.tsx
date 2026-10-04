/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { parse } from 'postcss'
import { describe, expect, it } from 'vitest'

import { HeroStarfield } from '../components/hero-starfield'

const stylesheet = parse(
  readFileSync(resolve('src/styles/gateway-home-showcase.css'), 'utf8')
)

function declarations(selector: string, media?: string) {
  const values: Record<string, string> = {}
  stylesheet.walkRules(selector, (rule) => {
    const parent = rule.parent
    const context = parent?.type === 'atrule' ? parent.params : undefined
    if (context !== media) return
    rule.walkDecls((declaration) => {
      values[declaration.prop] = declaration.value
    })
  })
  return values
}

describe('Hero background accessibility and layout', () => {
  it('keeps decorative stars outside the accessibility and keyboard navigation trees', () => {
    const { container } = render(<HeroStarfield />)
    expect(container.firstElementChild).toHaveAttribute('aria-hidden', 'true')
    expect(
      container.firstElementChild?.querySelector('button, a, [tabindex]')
    ).toBeNull()
    const styles = declarations('.gateway-starfield')
    expect(styles.position).toBe('absolute')
    expect(styles['pointer-events']).toBe('none')
    expect(styles.overflow).toBe('hidden')
  })

  it('spreads larger drifting stars across the full hero without a clipped center seam', () => {
    const { container } = render(<HeroStarfield />)
    expect(container.querySelectorAll('.gateway-star')).toHaveLength(32)
    expect(declarations('.gateway-starfield-side').width).toBe('50%')
    expect(declarations('.gateway-starfield-side').overflow).toBe('visible')
    expect(declarations('.gateway-star').width).toBe('6px')
    expect(declarations('.gateway-star:nth-child(5n + 1)').width).toBe('8px')
    expect(declarations('.gateway-star').animation).toBe(
      'gateway-star-drift 14s ease-in-out infinite alternate'
    )
    const animatedProperties = new Set<string>()
    const motionEndpoints: string[] = []
    stylesheet.walkAtRules('keyframes', (rule) => {
      if (rule.params !== 'gateway-star-drift') return
      rule.walkDecls((decl) => {
        animatedProperties.add(decl.prop)
        if (decl.prop === 'transform') motionEndpoints.push(decl.value)
      })
    })
    expect([...animatedProperties]).toEqual(['transform', 'opacity'])
    expect(motionEndpoints).toEqual([
      'translate(-32px, 48px)',
      'translate(42px, -72px)',
    ])
  })

  it('retains five distinct star colors in both themes independently of the brand accent', () => {
    const light = declarations('.gateway-starfield')
    const dark = declarations('.dark .gateway-starfield')
    const tokens = [
      '--star-gold',
      '--star-pink',
      '--star-violet',
      '--star-cyan',
      '--star-green',
    ]
    expect(light.color).toBe('var(--star-gold)')
    for (const palette of [light, dark]) {
      const colors = tokens.map((token) => palette[token])
      expect(colors.every((color) => /^#[0-9a-f]{6}$/i.test(color))).toBe(true)
      expect(new Set(colors).size).toBe(5)
    }
    for (const token of tokens) expect(dark[token]).not.toBe(light[token])
    expect(declarations('.gateway-star:nth-child(5n + 2)').color).toBe(
      'var(--star-pink)'
    )
    expect(declarations('.gateway-star:nth-child(5n + 3)').color).toBe(
      'var(--star-violet)'
    )
    expect(declarations('.gateway-star:nth-child(5n + 4)').color).toBe(
      'var(--star-cyan)'
    )
    expect(declarations('.gateway-star:nth-child(5n)').color).toBe(
      'var(--star-green)'
    )
  })

  it('lets keyboard users pause and resume stars without hiding the decoration', async () => {
    const user = userEvent.setup()
    const { container } = render(<HeroStarfield />)
    const pause = screen.getByRole('button', {
      name: 'Pause preview',
    })
    await user.tab()
    expect(pause).toHaveFocus()
    await user.keyboard('{Enter}')
    expect(
      screen.getByRole('button', { name: 'Play preview' })
    ).toHaveAttribute('aria-pressed', 'true')
    expect(container.firstElementChild).toHaveAttribute('data-paused', 'true')
    expect(
      declarations(".gateway-starfield[data-paused='true'] .gateway-star")[
        'animation-play-state'
      ]
    ).toBe('paused')
    await user.keyboard('{Enter}')
    expect(container.firstElementChild).toHaveAttribute('data-paused', 'false')
    expect(
      screen.getByRole('button', { name: 'Pause preview' })
    ).toHaveAttribute('aria-pressed', 'false')
  })

  it('keeps stars static when reduced motion is preferred', () => {
    const styles = declarations(
      '.gateway-star',
      '(prefers-reduced-motion: reduce)'
    )
    expect(styles.animation).toBe('none')
    expect(
      declarations(
        '.gateway-starfield-toggle',
        '(prefers-reduced-motion: reduce)'
      ).display
    ).toBe('none')
  })

  it('reduces mobile decoration to three static stars per edge outside the content', () => {
    expect(
      declarations('.gateway-starfield-side', '(max-width: 600px)').width
    ).toBe('16px')
    expect(
      declarations('.gateway-star:nth-child(n + 4)', '(max-width: 600px)')
        .display
    ).toBe('none')
    expect(declarations('.gateway-star', '(max-width: 600px)').animation).toBe(
      'none'
    )
  })
})
