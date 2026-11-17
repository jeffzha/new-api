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

/**
 * The super-administrator credit book. Credit is a platform-wide contract, so
 * the caller is always the site root user; limits and statement amounts stay
 * integer quota units on the wire and are rendered in CNY by the console.
 */
export interface CreditApiError {
  code?: string
  message?: string
  details?: unknown
}

export interface CreditApiResponse<T> {
  success: boolean
  data?: T
  error?: CreditApiError
  request_id?: string
}

export interface CreditAccount {
  user_id: string
  username: string
  account_name?: string
  configured: boolean
  status: string
  credit_limit: number
  used_quota: number
  available_quota: number
  payment_term_days?: number
  repayment_day?: number
  email_enabled?: boolean
  next_statement_at_ms?: number
  updated_at_ms?: number
}

export interface CreditStatement {
  id: number
  user_id: number
  username: string
  account_name?: string
  period: string
  amount_quota: number
  paid_quota: number
  bad_debt_quota: number
  remaining_quota: number
  status: string
  due_at_ms: number
  email_status: string
  payment_reference?: string
}

export interface CreditTermsPayload {
  credit_limit: number
  payment_term_days: number
  status: string
  email_enabled: boolean
}

export interface CreditPaymentPayload {
  amount_quota: number
  payment_reference: string
}
