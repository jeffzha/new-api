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

import {
  isWorkbenchCustomerRole,
  selectableWorkbenchSelectionToken,
  workbenchRoleLabelKey,
} from './workbench-role'

describe('workbench customer role contract', () => {
  test('accepts the complete supported role set', () => {
    for (const role of ['owner', 'admin', 'member', 'viewer']) {
      assert.equal(isWorkbenchCustomerRole(role), true)
    }
  })

  test('fails closed for removed and unknown roles', () => {
    for (const role of ['billing_admin', 'root', '', 'OWNER']) {
      assert.equal(isWorkbenchCustomerRole(role), false)
      assert.equal(workbenchRoleLabelKey(role), 'Unsupported customer role')
      assert.equal(
        selectableWorkbenchSelectionToken(role, 'opaque-token'),
        undefined
      )
    }
  })

  test('allows selection tokens only for supported roles', () => {
    assert.equal(
      selectableWorkbenchSelectionToken('viewer', 'opaque-token'),
      'opaque-token'
    )
  })

  test('maps every supported role to a distinct localized label key', () => {
    assert.deepEqual(
      ['owner', 'admin', 'member', 'viewer'].map(workbenchRoleLabelKey),
      [
        'Customer owner',
        'Customer administrator',
        'Customer member',
        'Customer viewer',
      ]
    )
  })
})
