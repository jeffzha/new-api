import { describe, expect, it } from 'vitest'

import { shouldAutoEnterAgencyCenter } from './agency-center'

describe('agency center sign-in routing', () => {
  it('lets an authenticated platform user attempt linked agency SSO', () => {
    expect(shouldAutoEnterAgencyCenter(true, 'agency')).toBe(true)
  })

  it('keeps an explicitly signed-out user on the agency sign-in page', () => {
    expect(shouldAutoEnterAgencyCenter(true, 'agency', true)).toBe(false)
  })

  it('never auto-enters without authentication or outside agency sign-in', () => {
    expect(shouldAutoEnterAgencyCenter(false, 'agency')).toBe(false)
    expect(shouldAutoEnterAgencyCenter(true, undefined)).toBe(false)
  })
})
