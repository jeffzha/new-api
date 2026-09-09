import { describe, expect, it } from 'vitest'

import { isExecutionAllowed } from './agent-store-deployments'

describe('Agent Store deployment execution gate', () => {
  it.each(['standard_v2', 'multi_agent_v2', 'workflow_v2', 'claw_static_v2', 'claw_dynamic_v2'])(
    'allows the verified supported runtime %s',
    (runtimeProfile) => {
      expect(isExecutionAllowed({ runtime_profile: runtimeProfile, status: 'verified' })).toBe(true)
      expect(isExecutionAllowed({ runtime_profile: runtimeProfile, status: 'active' })).toBe(true)
    },
  )

  it('fails closed for unknown, unverified, and disabled deployments', () => {
    expect(isExecutionAllowed({ runtime_profile: 'future_v3', status: 'verified' })).toBe(false)
    expect(isExecutionAllowed({ runtime_profile: 'claw_dynamic_v2', status: 'draft' })).toBe(false)
    expect(isExecutionAllowed({ runtime_profile: 'claw_dynamic_v2', status: 'disabled' })).toBe(false)
  })
})
