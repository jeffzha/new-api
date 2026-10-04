/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { render } from '@testing-library/react'
import { parse } from 'postcss'
import { describe, expect, it } from 'vitest'

import { HeroDataFlow } from '../components/hero-data-flow'

const stylesheet = parse(
  readFileSync(
    resolve('src/styles/gateway-home-showcase.css'),
    'utf8'
  )
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
  it('keeps decorative lines outside the accessibility and keyboard navigation trees', () => {
    const { container } = render(<HeroDataFlow />)
    expect(container.firstElementChild).toHaveAttribute('aria-hidden', 'true')
    expect(container.querySelector('button, a, [tabindex]')).toBeNull()
    const styles = declarations('.gateway-data-flow')
    expect(styles.position).toBe('absolute')
    expect(styles['pointer-events']).toBe('none')
    expect(styles.overflow).toBe('hidden')
  })

  it('keeps the center clear and ends the entrance motion within five seconds', () => {
    expect(declarations('.gateway-data-flow-side').width).toBe(
      'min(23%, 330px)'
    )
    const animation = declarations(
      '.gateway-data-rail-primary::after'
    ).animation
    expect(animation).toBe('gateway-data-travel 4.6s ease-in-out both')
    expect(animation).not.toContain('infinite')
  })

  it('removes moving segments when reduced motion is preferred', () => {
    const styles = declarations(
      '.gateway-data-rail-primary::after',
      '(prefers-reduced-motion: reduce)'
    )
    expect(styles.animation).toBe('none')
    expect(styles.display).toBe('none')
  })

  it('reduces mobile decoration to static edge lines without covering the content', () => {
    expect(
      declarations('.gateway-data-flow-side', '(max-width: 600px)').width
    ).toBe('52px')
    expect(
      declarations('.gateway-data-rail-primary::after', '(max-width: 600px)')
        .display
    ).toBe('none')
    expect(
      declarations('.gateway-data-rail-secondary', '(max-width: 600px)').display
    ).toBe('none')
  })
})
