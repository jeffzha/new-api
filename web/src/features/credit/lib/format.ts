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
import { formatQuotaWithCurrency, getCurrencyLabel } from '@/lib/currency'
import { parseQuotaFromDollars, quotaUnitsToEditableAmount } from '@/lib/format'

export const CREDIT_STATUS_ACTIVE = 'active'
export const CREDIT_STATUS_SUSPENDED = 'suspended'

/** The repayment day is fixed by the backend; the console only shows it. */
export const CREDIT_REPAYMENT_DAY = 1

/** Editable limit for the form, in the site's configured display currency. */
export function creditLimitToInput(quota: number | undefined): string {
  const value = Number(quota ?? 0)
  if (!Number.isFinite(value) || value <= 0) return ''
  return String(quotaUnitsToEditableAmount(value))
}

/**
 * Parse an operator-typed limit back into quota units. An empty field clears
 * the limit to zero; anything unparsable returns null so the form can refuse
 * to save instead of silently writing a wrong credit line.
 */
export function creditLimitFromInput(input: string): number | null {
  const trimmed = input.trim()
  if (trimmed === '') return 0
  if (!/^\d+(\.\d{1,6})?$/.test(trimmed)) return null
  const quota = parseQuotaFromDollars(Number(trimmed))
  return Number.isSafeInteger(quota) && quota >= 0 ? quota : null
}

export function creditCurrencyLabel(): string {
  return getCurrencyLabel()
}

export function formatCreditAmount(quota: number | undefined): string {
  return formatQuotaWithCurrency(quota ?? 0, {
    digitsLarge: 2,
    digitsSmall: 6,
    abbreviate: false,
  })
}

/** Statement statuses are stored as machine values and shown as labels. */
export function statementStatusKey(status: string): string {
  if (status === 'paid') return 'Settled'
  if (status === 'overdue') return 'Overdue'
  if (status === 'bad_debt') return 'Bad debt'
  return 'Issued'
}

export function emailStatusKey(status: string): string {
  if (status === 'sent') return 'Sent'
  if (status === 'failed') return 'Failed'
  return 'Not sent'
}

/** A statement can only be written off once it is past its due date. */
export function isStatementOverdue(statement: {
  status: string
}): boolean {
  return statement.status === 'overdue'
}

export function isStatementOpen(statement: { status: string }): boolean {
  return statement.status === 'issued' || statement.status === 'overdue'
}
