/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import { requireLocalSSORedirect } from './selection-redirect'

const origin = 'https://gateway.example.com'

describe('workbench selection SSO redirect', () => {
  test('accepts only the local SSO route with one non-empty ticket', () => {
    assert.equal(
      requireLocalSSORedirect('/workbench/auth/sso?ticket=opaque', origin),
      `${origin}/workbench/auth/sso?ticket=opaque`
    )
  })

  const unsafeRedirects = [
    'https://evil.example/workbench/auth/sso?ticket=opaque',
    '//evil.example/workbench/auth/sso?ticket=opaque',
    '/workbench/auth/sso',
    '/workbench/auth/sso?ticket=',
    '/workbench/auth/sso?ticket=opaque&next=%2F%2Fevil.example',
    '/workbench/auth/sso?ticket=opaque#fragment',
    '/other?ticket=opaque',
  ]
  for (const value of unsafeRedirects) {
    test(`rejects an unsafe redirect: ${value}`, () => {
      assert.throws(() => requireLocalSSORedirect(value, origin))
    })
  }
})
