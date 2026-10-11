import { z } from 'zod'

import type { InvoiceSource } from '../types'

export const headerSchema = z.object({
  purchaser_name: z.string().trim().min(1).max(255),
  tax_number: z.string().trim().min(1).max(64),
  receiver_email: z.string().trim().email().max(254),
  bank_name: z.string().max(255).optional(),
  bank_account: z.string().max(128).optional(),
  address: z.string().max(500).optional(),
  phone: z.string().max(64).optional(),
  remark: z.string().max(1000).optional(),
})

export type HeaderForm = z.infer<typeof headerSchema>

export function parseInvoiceCents(value: string): number | null {
  if (!/^\d+(?:\.\d{1,2})?$/.test(value.trim())) return null
  const [whole, fraction = ''] = value.trim().split('.')
  const cents = Number(whole) * 100 + Number(fraction.padEnd(2, '0'))
  return Number.isSafeInteger(cents) && cents > 0 ? cents : null
}

export function invoiceAllocations(
  sources: InvoiceSource[],
  amounts: Record<number, string>
) {
  const lines: { topup_id: number; allocated_cents: number }[] = []
  const errors: Record<number, string> = {}
  let totalCents = 0
  for (const source of sources) {
    const raw = amounts[source.topup_id]
    if (raw == null) continue
    const cents = parseInvoiceCents(raw)
    if (cents == null) {
      errors[source.topup_id] =
        'Enter a positive amount with up to two decimal places'
    } else if (cents > source.available_cents) {
      errors[source.topup_id] = 'Amount exceeds the available payment'
    } else if (!Number.isSafeInteger(totalCents + cents)) {
      errors[source.topup_id] = 'Invoice amount is too large'
    } else {
      totalCents += cents
      lines.push({ topup_id: source.topup_id, allocated_cents: cents })
    }
  }
  return {
    lines,
    errors,
    totalCents,
    valid: lines.length > 0 && Object.keys(errors).length === 0,
  }
}
