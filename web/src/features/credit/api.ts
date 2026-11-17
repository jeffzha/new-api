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
import { api } from '@/lib/api'

import type {
  CreditAccount,
  CreditApiResponse,
  CreditPaymentPayload,
  CreditStatement,
  CreditTermsPayload,
} from './types'

export async function listCreditAccounts(params: {
  query: string
  p: number
  page_size: number
}): Promise<
  CreditApiResponse<{
    items: CreditAccount[]
    total: number
    page?: number
    page_size?: number
  }>
> {
  const response = await api.get('/api/credit/terms', { params })
  return response.data
}

export async function resolveCreditAccount(
  query: string
): Promise<CreditApiResponse<CreditAccount>> {
  const response = await api.get('/api/credit/resolve-user', {
    params: { query },
  })
  return response.data
}

export async function getCreditAccount(
  userId: string
): Promise<CreditApiResponse<CreditAccount>> {
  const response = await api.get(
    `/api/credit/users/${encodeURIComponent(userId)}`
  )
  return response.data
}

export async function saveCreditAccount(
  userId: string,
  payload: CreditTermsPayload
): Promise<CreditApiResponse<CreditAccount>> {
  const response = await api.put(
    `/api/credit/users/${encodeURIComponent(userId)}`,
    payload
  )
  return response.data
}

export async function listCreditStatements(
  userId: string
): Promise<CreditApiResponse<{ items: CreditStatement[] }>> {
  const response = await api.get('/api/credit/statements', {
    params: { user_id: userId },
  })
  return response.data
}

export async function registerCreditPayment(
  statementId: number,
  payload: CreditPaymentPayload
): Promise<CreditApiResponse<CreditStatement>> {
  const response = await api.post(
    `/api/credit/statements/${statementId}/mark-paid`,
    payload
  )
  return response.data
}

export async function writeOffCreditStatement(
  statementId: number,
  reason: string
): Promise<CreditApiResponse<CreditStatement>> {
  const response = await api.post(
    `/api/credit/statements/${statementId}/mark-bad-debt`,
    { reason }
  )
  return response.data
}

export async function resendCreditStatementEmail(
  statementId: number
): Promise<CreditApiResponse<CreditStatement>> {
  const response = await api.post(
    `/api/credit/statements/${statementId}/resend-email`,
    {}
  )
  return response.data
}
