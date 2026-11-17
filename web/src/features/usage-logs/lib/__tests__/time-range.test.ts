/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { afterEach, expect, it, vi } from 'vitest'

import { getDefaultTimeRange } from '../utils'

afterEach(() => vi.useRealTimers())
it('ends the initial log range at entry time without including a future hour', () => {
  vi.useFakeTimers()
  vi.setSystemTime(new Date('2026-11-17T01:30:25Z'))
  const range = getDefaultTimeRange()
  expect(range.end.getTime()).toBe(Date.now())
  expect(range.start.getHours()).toBe(0)
})
