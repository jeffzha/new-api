import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, renderHook, waitFor } from '@testing-library/react'
import type { AxiosResponse, InternalAxiosRequestConfig } from 'axios'
import type { PropsWithChildren } from 'react'
import { afterEach, describe, expect, it } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'
import { useSystemConfigStore } from '@/stores/system-config-store'

import { usePricingData } from '../hooks/use-pricing-data'
import type { PricingData } from '../types'

const previousAdapter = api.defaults.adapter
const previousAuth = useAuthStore.getState()
const previousConfig = useSystemConfigStore.getState()
const clients: QueryClient[] = []

afterEach(() => {
  clients.forEach((client) => client.clear())
  clients.length = 0
  api.defaults.adapter = previousAdapter
  useAuthStore.setState(previousAuth, true)
  useSystemConfigStore.setState(previousConfig, true)
  localStorage.removeItem('status')
})

function pricingResponse(sales?: number): PricingData {
  return {
    success: true,
    data: [
      {
        id: 1,
        model_name: 'hy3',
        quota_type: 0,
        model_ratio: 2,
        completion_ratio: 3,
        enable_groups: ['default'],
        sales_bps: sales,
      },
    ],
    vendors: [],
    group_ratio: { default: 2 },
    usable_group: { default: { desc: 'Default', ratio: 2 } },
    supported_endpoint: {},
    auto_groups: [],
    pricing_scope: sales == null ? 'standard' : 'agency',
  }
}

function pricingHookWrapper(props: PropsWithChildren) {
  const client = clients.at(-1)
  if (!client) throw new Error('Pricing query client was not initialized')
  return (
    <QueryClientProvider client={client}>{props.children}</QueryClientProvider>
  )
}

describe('viewer-scoped customer price loading', () => {
  it('does not show a prior user price after switching accounts or signing out', async () => {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    clients.push(client)
    client.setQueryData(['status'], { price: 1, usd_exchange_rate: 1 })
    useAuthStore
      .getState()
      .auth.setUser({ id: 1, username: 'customer-one', role: 1 })
    const pending = new Map<
      number,
      {
        config: InternalAxiosRequestConfig
        resolve: (response: AxiosResponse) => void
      }
    >()
    api.defaults.adapter = (config) => {
      expect(config.url).toBe('/api/pricing')
      const userID = useAuthStore.getState().auth.user?.id ?? 0
      return new Promise((resolve) => {
        pending.set(userID, { config, resolve })
      })
    }
    const hook = renderHook(usePricingData, { wrapper: pricingHookWrapper })
    await waitFor(() => {
      expect(pending.has(1)).toBe(true)
    })
    await act(async () => {
      const request = pending.get(1)
      if (!request) throw new Error('Customer one pricing request is missing')
      request.resolve({
        config: request.config,
        status: 200,
        statusText: 'OK',
        headers: {},
        data: pricingResponse(9000),
      })
    })
    await waitFor(() => {
      expect(hook.result.current.models[0]?.sales_bps).toBe(9000)
    })
    act(() => {
      useAuthStore
        .getState()
        .auth.setUser({ id: 2, username: 'customer-two', role: 1 })
    })
    expect(hook.result.current.models).toEqual([])
    await waitFor(() => {
      expect(pending.has(2)).toBe(true)
    })
    await act(async () => {
      const request = pending.get(2)
      if (!request) throw new Error('Customer two pricing request is missing')
      request.resolve({
        config: request.config,
        status: 200,
        statusText: 'OK',
        headers: {},
        data: pricingResponse(12500),
      })
    })
    await waitFor(() => {
      expect(hook.result.current.models[0]?.sales_bps).toBe(12500)
    })
    act(() => {
      useAuthStore.getState().auth.setUser(null)
    })
    expect(hook.result.current.models).toEqual([])
    await waitFor(() => {
      expect(pending.has(0)).toBe(true)
    })
    await act(async () => {
      const request = pending.get(0)
      if (!request) throw new Error('Anonymous pricing request is missing')
      request.resolve({
        config: request.config,
        status: 200,
        statusText: 'OK',
        headers: {},
        data: pricingResponse(),
      })
    })
    await waitFor(() => {
      expect(hook.result.current.models).toHaveLength(1)
    })
    expect(hook.result.current.models[0].sales_bps).toBeUndefined()
    expect(hook.result.current.agencyPricing).toBe(false)
    hook.unmount()
  })

  it('hides previously loaded customer prices when policy reload fails', async () => {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    clients.push(client)
    client.setQueryData(['status'], { price: 1, usd_exchange_rate: 1 })
    useAuthStore
      .getState()
      .auth.setUser({ id: 1, username: 'customer-one', role: 1 })
    api.defaults.adapter = async (config) => ({
      config,
      status: 200,
      statusText: 'OK',
      headers: {},
      data: pricingResponse(9000),
    })
    const hook = renderHook(usePricingData, { wrapper: pricingHookWrapper })
    await waitFor(() => {
      expect(hook.result.current.models).toHaveLength(1)
    })
    api.defaults.adapter = async () => {
      throw new Error('Policy lookup unavailable')
    }
    await act(async () => {
      await hook.result.current.refetch()
    })
    await waitFor(() => {
      expect(hook.result.current.error).not.toBeNull()
    })
    expect(hook.result.current.models).toEqual([])
    hook.unmount()
  })
})
