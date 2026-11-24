import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { OPSAdjustDialog } from '../adjust-dialog'

const clients: QueryClient[] = []
afterEach(() => {
  cleanup()
  clients.splice(0).forEach((client) => client.clear())
  vi.restoreAllMocks()
  useAuthStore.getState().auth.reset()
  sessionStorage.clear()
})

it('retries an uncertain balance change with the same operation after reopening', async () => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  const post = vi
    .spyOn(api, 'post')
    .mockRejectedValueOnce(new Error('network unavailable'))
    .mockResolvedValueOnce({ data: { success: true } })
  const props = {
    open: true,
    onOpenChange: vi.fn(),
    userId: 15,
    onSuccess: vi.fn(),
  }
  const content = () => (
    <QueryClientProvider client={client}>
      <OPSAdjustDialog {...props} />
    </QueryClientProvider>
  )
  const view = render(content())
  await userEvent.type(screen.getByLabelText('Amount (CNY)'), '0.000001')
  await userEvent.type(screen.getByLabelText('Reason'), 'test grant')
  await userEvent.click(screen.getByRole('button', { name: 'Confirm' }))
  expect(
    await screen.findByText(
      'An adjustment is awaiting confirmation. Confirm again to retry the same operation.'
    )
  ).toBeVisible()
  expect(screen.getByLabelText('Amount (CNY)')).toBeDisabled()
  view.unmount()
  render(content())
  await userEvent.click(screen.getByRole('button', { name: 'Confirm' }))
  await vi.waitFor(() => expect(props.onSuccess).toHaveBeenCalledOnce())
  expect(post).toHaveBeenCalledTimes(2)
  expect(post.mock.calls[1][1]).toEqual(post.mock.calls[0][1])
  expect(post.mock.calls[0][1]).toMatchObject({
    id: 15,
    ops_amount: '0.000001',
    reason: 'test grant',
  })
  expect(sessionStorage.length).toBe(0)
})

it('allows editing a definitely rejected subtraction without retrying an uncertain change', async () => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  const post = vi.spyOn(api, 'post').mockResolvedValue({
    data: {
      success: false,
      retry_same_operation: false,
      message: 'Insufficient balance',
    },
  })
  render(
    <QueryClientProvider client={client}>
      <OPSAdjustDialog
        open
        onOpenChange={vi.fn()}
        userId={15}
        onSuccess={vi.fn()}
      />
    </QueryClientProvider>
  )
  await userEvent.click(screen.getByRole('button', { name: 'Subtract' }))
  await userEvent.type(screen.getByLabelText('Amount (CNY)'), '1')
  await userEvent.type(screen.getByLabelText('Reason'), 'test correction')
  await userEvent.click(screen.getByRole('button', { name: 'Confirm' }))
  await vi.waitFor(() => expect(post).toHaveBeenCalledOnce())
  await vi.waitFor(() =>
    expect(screen.getByLabelText('Amount (CNY)')).toBeEnabled()
  )
  expect(post.mock.calls[0][1]).toMatchObject({
    mode: 'subtract',
    ops_amount: '1',
  })
  expect(sessionStorage.length).toBe(0)
})

it('rejects zero additions but allows explicitly setting the balance to zero', async () => {
  const client = new QueryClient()
  clients.push(client)
  render(
    <QueryClientProvider client={client}>
      <OPSAdjustDialog
        open
        onOpenChange={vi.fn()}
        userId={15}
        onSuccess={vi.fn()}
      />
    </QueryClientProvider>
  )
  await userEvent.type(screen.getByLabelText('Amount (CNY)'), '0')
  await userEvent.type(screen.getByLabelText('Reason'), 'test reset')
  expect(screen.getByRole('button', { name: 'Confirm' })).toBeDisabled()
  expect(screen.getByLabelText('Amount (CNY)')).toHaveAttribute(
    'aria-invalid',
    'true'
  )
  await userEvent.click(screen.getByRole('button', { name: 'Override' }))
  expect(screen.getByRole('button', { name: 'Confirm' })).toBeEnabled()
})

it('a successful balance adjustment invalidates user details and the overview wallet snapshot', async () => {
  const client = new QueryClient()
  clients.push(client)
  client.setQueryData(['users', 'ops-balance', 15], { balance: '10' })
  client.setQueryData(['dashboard', 'wallet-balance', 15], { balance: '10' })
  vi.spyOn(api, 'post').mockResolvedValue({ data: { success: true } })
  const onSuccess = vi.fn()
  render(
    <QueryClientProvider client={client}>
      <OPSAdjustDialog
        open
        onOpenChange={vi.fn()}
        userId={15}
        onSuccess={onSuccess}
      />
    </QueryClientProvider>
  )
  await userEvent.type(screen.getByLabelText('Amount (CNY)'), '1')
  await userEvent.type(screen.getByLabelText('Reason'), 'test balance update')
  await userEvent.click(screen.getByRole('button', { name: 'Confirm' }))
  await vi.waitFor(() => expect(onSuccess).toHaveBeenCalledOnce())
  expect(
    client.getQueryState(['users', 'ops-balance', 15])?.isInvalidated
  ).toBe(true)
  expect(
    client.getQueryState(['dashboard', 'wallet-balance', 15])?.isInvalidated
  ).toBe(true)
})
