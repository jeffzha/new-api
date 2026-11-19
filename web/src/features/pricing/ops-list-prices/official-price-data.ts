/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import type { PricingModel } from '../types'
import type { ListPriceRow } from './types'

export function getOfficialRows(
  model: PricingModel
): ListPriceRow[] | undefined {
  if (model.list_price?.source !== 'ops') return undefined
  const rows = model.list_price.display_rows
  return rows?.length ? rows : undefined
}
