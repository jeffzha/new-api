import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { OpsBridgeSection } from '..'
import type { ImportPlan } from '../api'

const clients: QueryClient[] = []
afterEach(() => {
  clients.forEach((client) => client.clear())
  clients.length = 0
  vi.restoreAllMocks()
})

function renderBridge() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  clients.push(client)
  return render(
    <QueryClientProvider client={client}>
      <OpsBridgeSection />
    </QueryClientProvider>
  )
}

const plan: ImportPlan = {
  id: 'preview-1',
  catalog_version: 'catalog-1',
  before: { 'local-model': { configured: { ModelRatio: 0.5 } } },
  after: {
    'local-model': { 'billing_setting.billing_expr': 'tier("ops", p * 2)' },
  },
  protected_models: ['local-model'],
  unsupported_models: {},
}

it('requires explicit overwrite and confirmation before replacing a local price', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { enabled: true, deliveries: [] },
  })
  const post = vi.spyOn(api, 'post').mockResolvedValue({ data: plan })
  renderBridge()
  await userEvent.click(
    await screen.findByRole('button', { name: 'Preview official prices' })
  )
  const selection = await screen.findByRole('checkbox', {
    name: 'Select local-model',
  })
  expect(selection).not.toBeChecked()
  await userEvent.click(selection)
  const apply = screen.getByRole('button', { name: 'Import selected prices' })
  expect(apply).toBeDisabled()
  await userEvent.click(
    screen.getByRole('checkbox', { name: 'Replace local price local-model' })
  )
  await userEvent.click(apply)
  expect(post).toHaveBeenCalledTimes(1)
  const dialog = await screen.findByRole('alertdialog')
  await userEvent.click(within(dialog).getByRole('button', { name: 'Import' }))
  expect(await screen.findByRole('status')).toHaveTextContent(
    'Prices imported; confirmation queued'
  )
  expect(post).toHaveBeenCalledWith('/api/ops-bridge/imports/preview-1/apply', {
    models: ['local-model'],
    overwrite_models: ['local-model'],
  })
  expect(apply).toBeDisabled()
})

it('keeps import available after an apply failure without reporting success', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({ data: { enabled: true } })
  vi.spyOn(api, 'post').mockImplementation(async (path) => {
    if (path === '/api/ops-bridge/imports/preview') {
      return { data: { ...plan, protected_models: [] } }
    }
    throw new Error('Catalog changed')
  })
  renderBridge()
  await userEvent.click(
    await screen.findByRole('button', { name: 'Preview official prices' })
  )
  await userEvent.click(
    await screen.findByRole('button', { name: 'Import selected prices' })
  )
  const dialog = await screen.findByRole('alertdialog')
  await userEvent.click(within(dialog).getByRole('button', { name: 'Import' }))
  await waitFor(() =>
    expect(within(dialog).getByRole('button', { name: 'Import' })).toBeEnabled()
  )
  expect(
    screen.queryByText('Prices imported; confirmation queued')
  ).not.toBeInTheDocument()
})

it('shows disabled connection without offering write actions', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({ data: { enabled: false } })
  renderBridge()
  expect(await screen.findByRole('status')).toHaveTextContent(
    'Ops connection disabled'
  )
  expect(
    screen.queryByRole('button', { name: 'Preview official prices' })
  ).not.toBeInTheDocument()
})

it('queues local pricing evidence through the dedicated sync action', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({ data: { enabled: true } })
  const post = vi.spyOn(api, 'post').mockResolvedValue({ data: { queued: true } })
  renderBridge()
  await userEvent.click(
    await screen.findByRole('button', { name: 'Sync local prices to Ops' })
  )
  expect(post).toHaveBeenCalledWith('/api/ops-bridge/pricing/sync')
})

it('does not allow importing when the catalog has no supported prices', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({ data: { enabled: true } })
  vi.spyOn(api, 'post').mockResolvedValue({
    data: {
      ...plan,
      after: {},
      unsupported_models: { 'local-model': 'Unsupported call type' },
    },
  })
  renderBridge()
  await userEvent.click(
    await screen.findByRole('button', { name: 'Preview official prices' })
  )
  expect(await screen.findByText('Unsupported call type')).toBeVisible()
  expect(
    screen.getByRole('checkbox', { name: 'Select local-model' })
  ).toHaveAttribute('aria-disabled', 'true')
  expect(
    screen.getByRole('button', { name: 'Import selected prices' })
  ).toBeDisabled()
})
