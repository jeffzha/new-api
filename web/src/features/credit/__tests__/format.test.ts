import { describe, expect, it } from 'vitest'

import {
  creditLimitFromInput,
  isStatementOpen,
  isStatementOverdue,
  statementStatusKey,
} from '../lib/format'

describe('credit form values', () => {
  it('converts a valid currency input to quota units', () => {
    expect(creditLimitFromInput('12.50')).toBeGreaterThan(0)
  })

  it('rejects malformed and negative currency inputs', () => {
    expect(creditLimitFromInput('-1')).toBeNull()
    expect(creditLimitFromInput('12.1234567')).toBeNull()
    expect(creditLimitFromInput('abc')).toBeNull()
  })
})

describe('credit statement state', () => {
  it('only allows collection on open statements', () => {
    expect(isStatementOpen({ status: 'issued' })).toBe(true)
    expect(isStatementOpen({ status: 'overdue' })).toBe(true)
    expect(isStatementOpen({ status: 'paid' })).toBe(false)
  })

  it('only allows bad-debt write-off after the due date', () => {
    expect(isStatementOverdue({ status: 'overdue' })).toBe(true)
    expect(isStatementOverdue({ status: 'issued' })).toBe(false)
    expect(statementStatusKey('bad_debt')).toBe('Bad debt')
  })
})
