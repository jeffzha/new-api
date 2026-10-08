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

function styles(selector: string, mobile = false): Record<string, string> {
  const values: Record<string, string> = {}
  css.walkRules(selector, (rule) => {
    const parent = rule.parent
    if (
      parent?.type === 'atrule' &&
      (!mobile || parent.params !== '(max-width: 600px)')
    ) {
      return
    }
    rule.walkDecls((decl) => {
      values[decl.prop] = decl.value
    })
  })
  return values
}

describe('Homepage section layout', () => {
  it('keeps the bilingual regional notice readable on desktop and mobile', () => {
    expect(styles('.gateway-home .gateway-region-notice')).toMatchObject({
      display: 'grid',
      'grid-template-columns': 'auto minmax(0, 1fr)',
      'max-width': 'min(620px, 100%)',
      'text-align': 'left',
    })
    expect(styles('.gateway-home .gateway-region-notice', true)).toMatchObject({
      width: '100%',
      padding: '10px 12px',
    })
  })

  it('overrides the legacy mobile side rail with a horizontal step heading', () => {
    expect(styles('.gateway-home .gateway-step', true).display).toBe('flex')
    expect(
      styles('.gateway-home .gateway-step-top', true)['flex-direction']
    ).toBe('row')
  })
  it.each([false, true])(
    'centers step and feature contents (mobile: %s)',
    (mobile) => {
      for (const selector of [
        '.gateway-home .gateway-step',
        '.gateway-home .gateway-reason',
      ]) {
        expect(styles(selector, mobile)).toMatchObject({
          'align-items': 'center',
          'text-align': 'center',
        })
      }
      expect(styles('.gateway-home .gateway-step-connector').display).toBe(
        'none'
      )
    }
  )

  it('gives FAQ questions readable weight and room to wrap on desktop and mobile', () => {
    const selector = ".gateway-faq [data-slot='accordion-trigger']"
    expect(styles(selector)).toMatchObject({
      'font-size': '20px',
      'font-weight': '650',
      gap: '24px',
    })
    expect(styles(selector, true)).toMatchObject({
      'font-size': '18px',
      gap: '12px',
    })
    expect(styles('.gateway-faq-question')).toMatchObject({
      flex: '1',
      'min-width': '0',
      'overflow-wrap': 'anywhere',
    })
    expect(
      styles(".gateway-faq [data-slot='accordion-content'] p", true)[
        'font-size'
      ]
    ).toBe('16px')
  })
})
