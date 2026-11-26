import { z } from 'zod'

const money = z
  .string()
  .regex(/^[0-9]+([.][0-9]{1,2})?$/)
  .refine((v) => Number(v) > 0 && Number(v) <= 1_000_000)
export const cycleBudgetSchema = z
  .object({
    override: z.boolean(),
    amount: money,
    run: money.refine((v) => Number(v) <= 5),
    days: z
      .string()
      .refine(
        (v) => Number.isInteger(Number(v)) && Number(v) >= 1 && Number(v) <= 7
      ),
  })
  .refine((v) => Number(v.run) <= Number(v.amount), { path: ['run'] })
export type CycleBudgetValues = z.infer<typeof cycleBudgetSchema>
