import { describe, expect, it } from 'vitest'

import { shouldAutoEnterAgencyCenter } from './agency-center'

describe('agency center sign-in routing', () => {
  it('lets a super administrator enter through platform SSO', () => {
    expect(shouldAutoEnterAgencyCenter(true, 'agency')).toBe(true)
  })

  it('keeps an explicitly signed-out administrator on the agency sign-in page', () => {
    expect(shouldAutoEnterAgencyCenter(true, 'agency', true)).toBe(false)
  })

  it('never auto-enters for a non-administrator or a platform sign-in', () => {
    expect(shouldAutoEnterAgencyCenter(false, 'agency')).toBe(false)
    expect(shouldAutoEnterAgencyCenter(true, undefined)).toBe(false)
  })
})
