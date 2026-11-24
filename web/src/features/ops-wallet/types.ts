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
