/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import type { PricingModel } from '../types'

export type ListPriceInfo = {
  currency?: 'CNY' | 'USD'
  display_rows?: ListPriceRow[]
  source: 'ops' | 'local'
  comparison:
    | 'match'
    | 'differ'
    | 'review'
    | 'unavailable'
    | 'unsupported'
    | 'conversion_mismatch'
  official_cny?: Record<string, unknown>
  local: PricingModel
}

export type ListPriceRow = {
  when: Record<string, string | number | boolean>
  prices: Record<string, string>
}

export type ListPriceStatus = {
  version?: string
  checked_at?: string
  stale: boolean
  error?: string
}
