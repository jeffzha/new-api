import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { afterEach, expect, it, vi } from 'vitest'

import zh from '@/i18n/locales/zh.json'
import { api } from '@/lib/api'

import { AdminInvoiceReview } from '../components/admin-invoice-review'
import { ApplicationHistory } from '../components/application-history'
import { SourceSelector } from '../components/source-selector'
import type { InvoiceApplication, InvoiceSource } from '../types'

const clients: QueryClient[] = []
afterEach(() => {
  clients.forEach((client) => client.clear())
  clients.length = 0
  vi.restoreAllMocks()
})

function renderHistory(props: Parameters<typeof ApplicationHistory>[0] = {}) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  clients.push(client)
  return render(
    <QueryClientProvider client={client}>
      <ApplicationHistory {...props} />
    </QueryClientProvider>
  )
}

function renderAdmin() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  clients.push(client)
  return render(
    <QueryClientProvider client={client}>
      <AdminInvoiceReview />
    </QueryClientProvider>
  )
}

const application: InvoiceApplication = {
  id: 7,
  application_no: 'INV-7',
  status: 'pending_review',
  currency_code: 'CNY',
  total_cents: 1234,
  total: '12.34',
  purchaser_name: 'Example Buyer',
  tax_number: 'TEST',
  receiver_email: 'test@example.com',
  email_status: 'pending',
  created_at: 1,
  updated_at: 1,
  has_document: false,
  lines: [],
}

it('requires confirmation before withdrawing and refreshes the visible status', async () => {
  const get = vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: { items: [application], total: 1 } },
  })
  const post = vi.spyOn(api, 'post').mockImplementation(async () => {
    get.mockResolvedValue({
      data: {
        success: true,
        data: { items: [{ ...application, status: 'withdrawn' }], total: 1 },
      },
    })
    return { data: { success: true } }
  })
  renderHistory()
  await userEvent.click(await screen.findByRole('button', { name: 'Withdraw' }))
  expect(post).not.toHaveBeenCalled()
  const confirmation = await screen.findByRole('alertdialog')
  expect(confirmation).toHaveAccessibleName('Withdraw invoice application')
  await userEvent.click(
    within(confirmation).getByRole('button', { name: 'Withdraw' })
  )
  expect(await screen.findByText('Withdrawn')).toBeVisible()
  expect(post).toHaveBeenCalledWith('/api/invoice-applications/7/withdraw')
})

it('loads the next page instead of silently hiding older applications', async () => {
  vi.spyOn(api, 'get').mockImplementation(async (_url, config) => {
    const params = config?.params as { p?: number } | undefined
    return {
      data: {
        success: true,
        data: {
          total: 11,
          items: [
            {
              ...application,
              application_no: params?.p === 2 ? 'INV-OLD' : 'INV-7',
            },
          ],
        },
      },
    }
  })
  renderHistory()
  await screen.findByText('INV-7')
  expect(screen.getByRole('button', { name: 'Previous' })).toBeDisabled()
  await userEvent.click(screen.getByRole('button', { name: 'Next' }))
  expect(await screen.findByText('INV-OLD')).toBeVisible()
  expect(screen.getByRole('button', { name: 'Next' })).toBeDisabled()
})

it('shows a failed history request as an error rather than an empty invoice history', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: false, message: 'Unavailable' },
  })
  renderHistory()
  expect(await screen.findByText('Failed to load invoices')).toBeVisible()
  expect(
    screen.queryByText('No invoice applications yet')
  ).not.toBeInTheDocument()
})

it('opens user invoice details from history and shows buyer data', async () => {
  vi.spyOn(api, 'get').mockImplementation(async (url) => ({
    data: {
      success: true,
      data: url.endsWith('/7')
        ? application
        : { items: [application], total: 1 },
    },
  }))
  renderHistory()
  await userEvent.click(await screen.findByRole('button', { name: 'Details' }))
  expect(await screen.findByRole('dialog')).toHaveAccessibleName('INV-7')
  expect(await screen.findByText('test@example.com')).toBeVisible()
})

it('marks invalid source amounts accessibly and disables unselected amount inputs', async () => {
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
  const onToggle = vi.fn()
  const props = { sources: [source], amounts: {}, onChange: vi.fn(), onToggle }
  const result = render(<SourceSelector {...props} />)
  expect(
    screen.getByRole('textbox', { name: 'Invoice amount for paid-order' })
  ).toBeDisabled()
  await userEvent.click(
    screen.getByRole('checkbox', { name: 'Select invoice source paid-order' })
  )
  expect(onToggle).toHaveBeenCalledWith(1, true)
  result.rerender(
    <SourceSelector
      {...props}
      amounts={{ 1: '12.35' }}
      errors={{ 1: 'Amount exceeds the available payment' }}
    />
  )
  await waitFor(() =>
    expect(screen.getByRole('textbox')).toHaveAttribute('aria-invalid', 'true')
  )
  expect(screen.getByRole('textbox')).toHaveAccessibleDescription(
    'Amount exceeds the available payment'
  )
})

it('requires administrator confirmation before approving an application', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: { items: [application], total: 1 } },
  })
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true } })
  renderAdmin()
  await userEvent.click(await screen.findByRole('button', { name: 'Approve' }))
  expect(post).not.toHaveBeenCalled()
  const dialog = await screen.findByRole('alertdialog', {
    name: 'Approve invoice application',
  })
  await userEvent.click(within(dialog).getByRole('button', { name: 'Approve' }))
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith(
      '/api/invoice-applications/admin/7/review',
      { approved: true, reason: '' }
    )
  )
})

it('requires a reason before an administrator can reject an application', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: { items: [application], total: 1 } },
  })
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true } })
  renderAdmin()
  await userEvent.click(await screen.findByRole('button', { name: 'Reject' }))
  const dialog = await screen.findByRole('dialog', {
    name: 'Reject invoice application',
  })
  expect(within(dialog).getByRole('button', { name: 'Confirm' })).toBeDisabled()
  await userEvent.type(
    within(dialog).getByRole('textbox', { name: /Reason/ }),
    'Incorrect buyer'
  )
  await userEvent.click(within(dialog).getByRole('button', { name: 'Confirm' }))
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith(
      '/api/invoice-applications/admin/7/review',
      { approved: false, reason: 'Incorrect buyer' }
    )
  )
})

it('blocks issue until an invoice number, date and PDF are supplied', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: { items: [{ ...application, status: 'approved' }], total: 1 },
    },
  })
  renderAdmin()
  await userEvent.click(
    await screen.findByRole('button', { name: 'Issue invoice' })
  )
  const dialog = await screen.findByRole('dialog', { name: 'Issue invoice' })
  const confirm = within(dialog).getByRole('button', { name: 'Confirm issue' })
  expect(confirm).toBeDisabled()
  await userEvent.type(
    within(dialog).getByLabelText(/Invoice number/),
    'REAL-1'
  )
  await userEvent.type(
    within(dialog).getByLabelText(/Invoice date/),
    '2026-10-10'
  )
  expect(confirm).toBeDisabled()
  await userEvent.upload(
    within(dialog).getByLabelText(/Invoice PDF/),
    new File(['%PDF-1.7 test'], 'invoice.pdf', { type: 'application/pdf' })
  )
  expect(confirm).toBeEnabled()
})

it('keeps translated all and rejected labels on the Chinese status trigger after the menu closes', async () => {
  const locale = createInstance()
  await locale.init({ lng: 'zh', initAsync: false, resources: { zh } })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: { items: [], total: 0 } },
  })
  render(
    <I18nextProvider i18n={locale}>
      <QueryClientProvider client={client}>
        <ApplicationHistory />
      </QueryClientProvider>
    </I18nextProvider>
  )
  const trigger = await screen.findByRole('combobox', {
    name: locale.t('Invoice status'),
  })
  expect(trigger).toHaveTextContent('全部')
  await userEvent.click(trigger)
  await userEvent.click(await screen.findByRole('option', { name: '已驳回' }))
  await waitFor(() => {
    const selected = screen.getByRole('combobox', { name: locale.t('Invoice status') })
    expect(selected).toHaveTextContent('已驳回')
    expect(selected).not.toHaveTextContent('rejected')
  })
})
