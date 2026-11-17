import { expect, it } from 'vitest'

import {
  headerSchema,
  invoiceAllocations,
  parseInvoiceCents,
} from '../lib/validation'
import type { InvoiceSource } from '../types'

const source: InvoiceSource = {
  topup_id: 1,
  trade_no: 'paid-order',
  paid_cents: 1234,
  available_cents: 1234,
  paid: '12.34',
  available: '12.34',
  currency_code: 'CNY',
  paid_at: 1,
}

it('converts decimal money to exact cents without floating point rounding', () => {
  expect(parseInvoiceCents('1.13')).toBe(113)
  expect(parseInvoiceCents('12.3')).toBe(1230)
})

it.each(['', '0', '-1', '1.001', '1e3', 'Infinity', '9007199254740992'])(
  'rejects unsafe or invalid amount %s',
  (value) => {
    expect(parseInvoiceCents(value)).toBeNull()
  }
)

it('blocks an overallocated source and reports an inline error', () => {
  expect(invoiceAllocations([source], { 1: '12.35' })).toMatchObject({
    valid: false,
    errors: { 1: 'Amount exceeds the available payment' },
  })
})

it('blocks a selected source with a cleared amount', () => {
  expect(invoiceAllocations([source], { 1: '' }).valid).toBe(false)
})

it('uses current eligible sources and exact totals when creating allocation lines', () => {
  expect(invoiceAllocations([source], { 1: '12.34', 2: '100' })).toMatchObject({
    valid: true,
    totalCents: 1234,
    lines: [{ topup_id: 1, allocated_cents: 1234 }],
  })
})

it('rejects incomplete buyer details and invalid receiving email', () => {
  expect(
    headerSchema.safeParse({
      purchaser_name: ' ',
      tax_number: '',
      receiver_email: 'bad-email',
    }).success
  ).toBe(false)
})
