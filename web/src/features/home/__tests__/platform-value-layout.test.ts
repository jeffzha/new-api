/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { parse } from 'postcss'
import { describe, expect, it } from 'vitest'

describe('Platform value API key node', () => {
  it('centers the icon and label in a stable, symmetrically padded background', () => {
    const css = parse(
      readFileSync(resolve('src/styles/gateway-home.css'), 'utf8')
    )
    const styles: Record<string, string> = {}
    css.walkRules('.value-key', (rule) => {
      rule.walkDecls((decl) => {
        styles[decl.prop] = decl.value
      })
    })
    expect(styles['align-items']).toBe('center')
    expect(styles['justify-content']).toBe('center')
    expect(styles.width).toBe('58px')
    expect(styles.height).toBe('56px')
    expect(styles.padding).toBe('6px')
    expect(styles['padding-right']).toBeUndefined()
  })
})
