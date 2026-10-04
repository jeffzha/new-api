/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { parse } from 'postcss'
import { describe, expect, it } from 'vitest'

const css = parse(
  readFileSync(resolve('src/styles/gateway-home-showcase.css'), 'utf8')
)

function declarations(selector: string, context?: string) {
  const values: Record<string, string> = {}
  css.walkRules(selector, (rule) => {
    const parent = rule.parent
    if ((parent?.type === 'atrule' ? parent.params : undefined) !== context) {
      return
    }
    rule.walkDecls((decl) => {
      values[decl.prop] = decl.value
    })
  })
  return values
}

describe('Homepage typography color hierarchy', () => {
  it.each(['prism-console', 'signal-console'])(
    'gives the %s headline distinct gradient stops in light and dark modes',
    (preset) => {
      for (const mode of ['', '.dark ']) {
        const palette = declarations(
          `${mode}body[data-theme-preset='${preset}'] .gateway-home`
        )
        const colors = ['start', 'middle', 'end'].map(
          (stop) => palette[`--home-title-${stop}`]
        )
        expect(colors.every((color) => /^#[\da-f]{6}$/i.test(color))).toBe(true)
        expect(new Set(colors).size).toBe(3)
      }
    }
  )

  it.each([
    ['prism-console.css', false],
    ['prism-console.css', true],
    ['signal-workspace.css', false],
    ['signal-workspace.css', true],
  ] as const)('keeps accents readable in %s (dark: %s)', (file, dark) => {
    const theme = parse(readFileSync(resolve('src/styles', file), 'utf8'))
    const palette: Record<string, number[]> = {}
    theme.walkRules((rule) => {
      if (rule.selector.includes('.dark') !== dark) return
      for (const node of rule.nodes) {
        if (node.type !== 'decl' || !/^#[\da-f]{6}$/i.test(node.value)) continue
        palette[node.prop] = [1, 3, 5].map(
          (offset) =>
            Number.parseInt(node.value.slice(offset, offset + 2), 16) / 255
        )
      }
    })
    const luminance = (channels: number[]) => {
      const linear = channels.map((v) =>
        v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4
      )
      return linear[0] * 0.2126 + linear[1] * 0.7152 + linear[2] * 0.0722
    }
    const accents = [
      ...['start', 'middle', 'end'].map((stop) => {
        const preset =
          file === 'prism-console.css' ? 'prism-console' : 'signal-console'
        const title = declarations(
          `${dark ? '.dark ' : ''}body[data-theme-preset='${preset}'] .gateway-home`
        )
        return [1, 3, 5].map(
          (offset) =>
            Number.parseInt(
              title[`--home-title-${stop}`].slice(offset, offset + 2),
              16
            ) / 255
        )
      }),
      palette['--primary'],
      ...['--chart-2', '--chart-3'].map((token) =>
        palette[token].map(
          (channel, index) =>
            channel * 0.65 + palette['--foreground'][index] * 0.35
        )
      ),
    ]
    for (const accent of accents) {
      for (const surface of ['--background', '--muted', '--card']) {
        const a = luminance(accent)
        const b = luminance(palette[surface])
        expect(
          (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05)
        ).toBeGreaterThanOrEqual(4.5)
      }
    }
  })

  it('inherits emphasis colors from the active theme in both light and dark modes', () => {
    const palette = declarations('.gateway-home')
    expect(palette['--home-text-primary']).toBe('var(--primary)')
    expect(palette['--home-text-secondary'].replaceAll(/\s/g, '')).toBe(
      'color-mix(insrgb,var(--chart-2)65%,var(--foreground))'
    )
    expect(palette['--home-text-tertiary'].replaceAll(/\s/g, '')).toBe(
      'color-mix(insrgb,var(--chart-3)65%,var(--foreground))'
    )
    expect(declarations('.dark .gateway-home')).toEqual({})
  })

  it('uses a theme gradient with solid fallback and forced-color support', () => {
    expect(declarations('.gateway-home #gateway-title').color).toBe(
      'var(--home-text-primary)'
    )
    const gradient = declarations(
      '.gateway-home #gateway-title',
      '(background-clip: text) or (-webkit-background-clip: text)'
    )
    expect(gradient['background-image']).toContain('var(--home-title-start)')
    expect(gradient['background-image']).toContain('var(--home-title-middle)')
    expect(gradient['background-image']).toContain('var(--home-title-end)')
    expect(declarations('.gateway-home #gateway-title').width).toBe(
      'fit-content'
    )
    expect(gradient['background-clip']).toBe('text')
    expect(gradient.color).toBe('transparent')
    const forced = declarations(
      '.gateway-home #gateway-title',
      '(forced-colors: active)'
    )
    expect(forced.color).toBe('CanvasText')
    expect(forced['background-image']).toBe('none')
  })

  it('uses separate value accents while keeping description copy neutral', () => {
    expect(
      declarations('.gateway-home .gateway-value-access')['--value-accent']
    ).toBe('var(--home-text-primary)')
    expect(
      declarations('.gateway-home .gateway-value-reliability')['--value-accent']
    ).toBe('var(--home-text-secondary)')
    expect(
      declarations('.gateway-home .gateway-value-budget')['--value-accent']
    ).toBe('var(--home-text-tertiary)')
    expect(declarations('.gateway-home .gateway-hero-detail').color).toBe(
      'var(--home-muted)'
    )
  })
})
