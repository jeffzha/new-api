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
import { assert, describe, test } from 'vitest'

import { agentStoreApi, agentStoreCSRFToken } from './api'

describe('Agent Store CSRF cookie', () => {
  test('reads and decodes only the dedicated control token', () => {
    assert.equal(
      agentStoreCSRFToken(
        'other=value; claw_control_csrf=token%2Bbound%3D; ignored=later'
      ),
      'token+bound='
    )
  })

  test('fails closed for missing, empty, or malformed tokens', () => {
    assert.equal(agentStoreCSRFToken('other=value'), undefined)
    assert.equal(agentStoreCSRFToken('claw_control_csrf='), undefined)
    assert.equal(agentStoreCSRFToken('claw_control_csrf=%E0%A4%A'), undefined)
  })
})

describe('Agent Store catalog contract', () => {
  test('binds opaque pagination and server-side filters to the request URL', async () => {
    const originalFetch = globalThis.fetch
    let requestedURL = ''
    globalThis.fetch = (input) => {
      requestedURL = String(input)
      return Promise.resolve(
        new Response(
          JSON.stringify({
            success: true,
            data: { items: [], next_cursor: 'next' },
          }),
          { status: 200, headers: { 'Content-Type': 'application/json' } }
        )
      )
    }
    try {
      const result = await agentStoreApi.list({
        cursor: 'opaque+/=',
        category: '办公 助手',
        query: '合同 & 审核',
      })
      assert.equal(result.next_cursor, 'next')
      assert.equal(
        requestedURL,
        '/api/workbench/agent-store?cursor=opaque%2B%2F%3D&category=%E5%8A%9E%E5%85%AC+%E5%8A%A9%E6%89%8B&query=%E5%90%88%E5%90%8C+%26+%E5%AE%A1%E6%A0%B8'
      )
    } finally {
      globalThis.fetch = originalFetch
    }
  })
})
