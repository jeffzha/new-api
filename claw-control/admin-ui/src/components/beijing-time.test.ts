import { describe, expect, it } from 'vitest'

import {
  formatBeijingDate,
  parseBeijingDateTimeLocal,
  toBeijingDateTimeLocal,
} from './beijing-time'

describe('Beijing time contract', () => {
  it('interprets datetime-local values as fixed UTC+08:00 wall-clock time', () => {
    expect(parseBeijingDateTimeLocal('2026-08-10T16:30')).toBe(
      '2026-08-10T08:30:00.000Z',
    )
    expect(parseBeijingDateTimeLocal('2026-01-01T00:15:30.25')).toBe(
      '2025-12-31T16:15:30.250Z',
    )
    expect(parseBeijingDateTimeLocal('')).toBeUndefined()
  })

  it('rejects malformed or normalized calendar values', () => {
    expect(() => parseBeijingDateTimeLocal('2026-02-30T12:00')).toThrow(
      RangeError,
    )
    expect(() => parseBeijingDateTimeLocal('2026-08-10 12:00')).toThrow(
      RangeError,
    )
  })

  it('builds datetime-local defaults in Beijing regardless of host timezone', () => {
    expect(toBeijingDateTimeLocal(new Date('2026-08-10T16:30:00.000Z'))).toBe(
      '2026-08-11T00:30',
    )
  })

  it('always renders timestamps in the Asia/Shanghai timezone', () => {
    expect(formatBeijingDate('2026-08-10T00:30:00.000Z', 'zh')).toContain(
      '08:30',
    )
    expect(formatBeijingDate('not-a-date', 'zh')).toBe('not-a-date')
    expect(formatBeijingDate()).toBe('—')
  })
})
