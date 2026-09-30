/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { describe, expect, it } from 'vitest'

import { resolveDocumentationLink } from '../lib/documentation-link'

const origin = 'https://gateway.example.com'

describe('Configured documentation destination', () => {
  it('preserves the configured public documentation path', () => {
    expect(
      resolveDocumentationLink(
        ' https://docs.example.com/start?q=api#keys ',
        origin
      )
    ).toBe('https://docs.example.com/start?q=api#keys')
    expect(resolveDocumentationLink('/guide?lang=zh#quickstart', origin)).toBe(
      '/guide?lang=zh#quickstart'
    )
  })

  it.each([
    undefined,
    '',
    '/docs',
    '/docs/?next=https://evil.example',
    `${origin}/docs#api`,
    'javascript:alert(1)',
    'data:text/html,test',
    '//evil.example',
    '/\\evil.example',
    'https://user:password@docs.example.com',
    'https://docs.example.com\n/guide',
  ])(
    'falls back to bundled docs for unsafe or looping target %s',
    (configured) => {
      expect(resolveDocumentationLink(configured, origin)).toBeNull()
    }
  )
})
