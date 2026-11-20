/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { z } from 'zod'

export const opsWalletFields = {
  wallet_authority: z.literal('ops').optional(),
  wallet_adjustments_enabled: z.boolean().optional(),
  ops_balance: z
    .object({
      status: z.enum(['pending', 'unavailable', 'available', 'stale']),
      amount: z.string().nullable(),
      quota: z.number().nullable(),
      currency: z.string(),
      stale: z.boolean(),
      checked_at: z.string().optional(),
    })
    .optional(),
}

export type OPSBalance = z.infer<typeof opsWalletFields.ops_balance>
export type OPSWalletFields = {
  wallet_authority?: 'ops'
  wallet_adjustments_enabled?: boolean
  ops_balance?: OPSBalance
}
