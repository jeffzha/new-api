import { describe, expect, it } from 'vitest'

import type { CredentialProfile } from '../api/contracts'
import { activePlatformCredentials } from './agent-store-listing-dialog'

function credential(overrides: Partial<CredentialProfile> = {}): CredentialProfile {
  return {
    id: 1,
    owner_scope: 'platform',
    provider_environment: 'china_tencent_adp',
    name: 'platform-adp',
    fingerprint: 'sha256:test',
    fingerprint_version: 1,
    status: 'active',
    version: 1,
    row_version: 1,
    ...overrides,
  }
}

describe('Agent Store listing platform credential gate', () => {
  it('accepts only active platform-scoped credentials', () => {
    const eligible = activePlatformCredentials([
      credential({ id: 1 }),
      credential({ id: 2, owner_scope: 'customer:1', customer_id: 1 }),
      credential({ id: 3, status: 'staged' }),
    ])

    expect(eligible.map((profile) => profile.id)).toEqual([1])
  })

  it('reports no eligible credential for a customer-only credential list', () => {
    expect(activePlatformCredentials([
      credential({ owner_scope: 'customer:1', customer_id: 1 }),
    ])).toEqual([])
  })
})
