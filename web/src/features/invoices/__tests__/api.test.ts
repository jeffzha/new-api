import { Blob as NodeBlob } from 'node:buffer'

import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { getInvoicePDF, issueInvoiceApplication } from '../api'

const originalAdapter = api.defaults.adapter
afterEach(() => {
  api.defaults.adapter = originalAdapter
  useAuthStore.getState().auth.reset()
  localStorage.clear()
  vi.restoreAllMocks()
})

it('downloads private PDFs with the current bearer token through the authenticated client', async () => {
  useAuthStore.setState((state) => ({
    auth: { ...state.auth, accessToken: 'local-test-token' },
  }))
  const adapter = vi.fn(async (config) => ({
    data: new NodeBlob(['%PDF-1.7 test'], { type: 'application/pdf' }),
    status: 200,
    statusText: 'OK',
    headers: {},
    config,
  }))
  api.defaults.adapter = adapter
  const blob = await getInvoicePDF(7)
  expect(blob.type).toBe('application/pdf')
  expect(adapter.mock.calls[0][0].headers.get('Authorization')).toBe(
    'Bearer local-test-token'
  )
  expect(adapter.mock.calls[0][0].url).toBe(
    '/api/invoice-applications/7/document'
  )
})

it('rejects a business error returned by the PDF endpoint instead of downloading JSON', async () => {
  api.defaults.adapter = async (config) => ({
    data: new NodeBlob(['{"success":false,"message":"Not your invoice"}'], {
      type: 'application/json',
    }),
    status: 200,
    statusText: 'OK',
    headers: {},
    config,
  })
  await expect(getInvoicePDF(7)).rejects.toThrow('Not your invoice')
})

it('uploads multipart data without forcing a JSON content type and sends the exact amount', async () => {
  const adapter = vi.fn(async (config) => ({
    data: { success: true },
    status: 200,
    statusText: 'OK',
    headers: {},
    config,
  }))
  api.defaults.adapter = adapter
  await issueInvoiceApplication(7, {
    amountCents: 1234,
    invoiceNumber: 'REAL-1',
    invoiceCode: '',
    invoiceDate: '2026-10-10',
    file: new File(['%PDF-1.7 test'], 'invoice.pdf', {
      type: 'application/pdf',
    }),
  })
  const config = adapter.mock.calls[0][0]
  expect(config.data).toBeInstanceOf(FormData)
  expect(config.data.get('amount_cents')).toBe('1234')
  expect(config.headers.get('Content-Type')).not.toBe('application/json')
})
