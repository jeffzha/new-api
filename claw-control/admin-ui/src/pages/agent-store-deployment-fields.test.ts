import { describe, expect, it } from 'vitest'

import { parseEntitlements } from './agent-store-deployment-fields'

describe('Agent Store entitlement form contract', () => {
  it('preserves repeated subjects and converts Beijing validity windows to ISO-8601', () => {
    const form = new FormData()
    form.append('entitlement_subject_type', 'user')
    form.append('entitlement_subject_ref', '101')
    form.append('entitlement_valid_from', '2026-08-11T08:00')
    form.append('entitlement_valid_until', '2026-09-11T08:00')
    form.append('entitlement_subject_type', 'role')
    form.append('entitlement_subject_ref', 'member')
    form.append('entitlement_valid_from', '')
    form.append('entitlement_valid_until', '')

    expect(parseEntitlements(form)).toEqual([
      {
        subject_type: 'user',
        subject_ref: '101',
        valid_from: '2026-08-11T00:00:00.000Z',
        valid_until: '2026-09-11T00:00:00.000Z',
      },
      { subject_type: 'role', subject_ref: 'member' },
    ])
  })

  it('returns an empty list so the server can create the default customer entitlement', () => {
    expect(parseEntitlements(new FormData())).toEqual([])
  })
})
