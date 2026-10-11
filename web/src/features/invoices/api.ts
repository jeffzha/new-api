import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'

import type {
  ApiResponse,
  InvoiceApplication,
  InvoiceApplicationPayload,
  InvoiceSource,
  InvoiceSummary,
  Paginated,
} from './types'

export async function getInvoiceSummary(): Promise<
  ApiResponse<InvoiceSummary>
> {
  const response = await api.get('/api/invoice-applications/summary')
  return response.data
}

export async function getInvoiceSources(): Promise<
  ApiResponse<InvoiceSource[]>
> {
  const response = await api.get('/api/invoice-applications/sources')
  return response.data
}

export async function getInvoiceApplications(
  page = 1,
  pageSize = 10,
  status = ''
): Promise<ApiResponse<Paginated<InvoiceApplication>>> {
  const response = await api.get('/api/invoice-applications', {
    params: { p: page, page_size: pageSize, status: status || undefined },
  })
  return response.data
}

export async function getInvoiceApplication(
  id: number,
  admin = false
): Promise<ApiResponse<InvoiceApplication>> {
  const response = await api.get(
    admin
      ? `/api/invoice-applications/admin/${id}`
      : `/api/invoice-applications/${id}`
  )
  return response.data
}

export async function createInvoiceApplication(
  payload: InvoiceApplicationPayload
): Promise<ApiResponse<InvoiceApplication>> {
  const response = await api.post('/api/invoice-applications', payload)
  return response.data
}

export async function withdrawInvoiceApplication(
  id: number
): Promise<ApiResponse> {
  const response = await api.post(`/api/invoice-applications/${id}/withdraw`)
  return response.data
}

export async function requestInvoiceVoid(
  id: number,
  reason: string
): Promise<ApiResponse> {
  const response = await api.post(
    `/api/invoice-applications/${id}/void-request`,
    { reason, approved: false }
  )
  return response.data
}

export async function getAdminInvoiceApplications(
  page = 1,
  pageSize = 20,
  status = ''
): Promise<ApiResponse<Paginated<InvoiceApplication>>> {
  const response = await api.get('/api/invoice-applications/admin', {
    params: { p: page, page_size: pageSize, status: status || undefined },
  })
  return response.data
}

export async function reviewInvoiceApplication(
  id: number,
  approved: boolean,
  reason: string
): Promise<ApiResponse> {
  const response = await api.post(
    `/api/invoice-applications/admin/${id}/review`,
    { approved, reason }
  )
  return response.data
}

export async function issueInvoiceApplication(
  id: number,
  payload: {
    amountCents: number
    invoiceNumber: string
    invoiceCode: string
    invoiceDate: string
    file: File
  }
): Promise<ApiResponse> {
  const form = new FormData()
  form.append('amount_cents', String(payload.amountCents))
  form.append('invoice_number', payload.invoiceNumber)
  form.append('invoice_code', payload.invoiceCode)
  form.append(
    'invoice_date',
    String(Math.floor(new Date(payload.invoiceDate).getTime() / 1000))
  )
  form.append('document', payload.file)
  const response = await api.post(
    `/api/invoice-applications/admin/${id}/issue`,
    form,
    { headers: { 'Content-Type': undefined } }
  )
  return response.data
}

export async function reviewInvoiceVoid(
  id: number,
  approved: boolean,
  reason: string
): Promise<ApiResponse> {
  const response = await api.post(
    `/api/invoice-applications/admin/${id}/void-review`,
    { approved, reason }
  )
  return response.data
}

export async function resendInvoiceEmail(id: number): Promise<ApiResponse> {
  const response = await api.post(
    `/api/invoice-applications/admin/${id}/resend`
  )
  return response.data
}

export async function getInvoicePDF(id: number, admin = false): Promise<Blob> {
  const url = admin
    ? `/api/invoice-applications/admin/${id}/document`
    : `/api/invoice-applications/${id}/document`
  const response = await api.get<Blob>(url, { responseType: 'blob' })
  if (!response.data.type.startsWith('application/pdf')) {
    const errorResponse: ApiResponse = JSON.parse(await response.data.text())
    requireServerSuccess(errorResponse)
    throw new Error('Invalid invoice PDF response')
  }
  return response.data
}
