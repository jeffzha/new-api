/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { z } from 'zod'

import { creditLimitFromInput } from './format'

export const creditTermsSchema = z.object({
  limit: z.string().refine((value) => creditLimitFromInput(value) !== null),
  termDays: z
    .string()
    .refine(
      (value) =>
        value.trim() !== '' &&
        Number.isInteger(Number(value)) &&
        Number(value) >= 0 &&
        Number(value) <= 365
    ),
  status: z.enum(['active', 'suspended']),
  emailEnabled: z.boolean(),
})

export type CreditTermsFormValues = z.infer<typeof creditTermsSchema>
