import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AxiosError, AxiosHeaders } from 'axios'
import { afterEach, expect, it } from 'vitest'

import { api } from '@/lib/http-client'

import { HealthNotifications, HealthRules } from '../settings'

const originalAdapter = api.defaults.adapter

afterEach(() => {
  api.defaults.adapter = originalAdapter
})

it('paginates notification recipients and deliveries independently', async () => {
  api.defaults.adapter = async (config) => {
    const page = Number(config.params.page)
    const recipient = config.url?.endsWith('/notifications')
    const items = recipient
      ? [{ id: `recipient-${page}`, name: `Recipient ${page}`, kind: 'feishu' }]
      : [{ id: `delivery-${page}`, target: `Delivery ${page}`, state: 'sent' }]
    return {
      config,
      status: 200,
      statusText: 'OK',
      headers: {},
      data: { success: true, data: { items, has_more: page === 1 } },
    }
  }
  const client = new QueryClient()
  const user = userEvent.setup()
  const view = render(
    <QueryClientProvider client={client}>
      <HealthNotifications worker={false} />
    </QueryClientProvider>
  )
  try {
    const recipients = within(
      await screen.findByRole('region', { name: 'Destinations' })
    )
    const deliveries = within(
      await screen.findByRole('region', { name: 'Delivery history' })
    )
    await user.click(recipients.getByRole('button', { name: 'Next page' }))
    expect(await screen.findByText('Recipient 2')).toBeVisible()
    expect(deliveries.getByText('Delivery 1')).toBeVisible()
    await user.click(deliveries.getByRole('button', { name: 'Next page' }))
    expect(await screen.findByText('Delivery 2')).toBeVisible()
    expect(screen.getByText('Recipient 2')).toBeVisible()
    expect(
      within(
        screen.getByRole('region', { name: 'Delivery history' })
      ).getByRole('button', { name: 'Next page' })
    ).toBeDisabled()
  } finally {
    view.unmount()
    client.clear()
  }
})

it('shows a rule read limit and loads the template on retry without requesting the budget', async () => {
  let templateLimited = true
  api.defaults.adapter = async (config) => {
    expect(config.url).not.toMatch(/\/budget$/)
    const isTemplate = config.url?.endsWith('/template')
    const headers = new AxiosHeaders()
    if (isTemplate && templateLimited) {
      headers.set('Retry-After', '60')
      throw new AxiosError(
        'Request failed with status code 429',
        undefined,
        config,
        undefined,
        {
          config,
          data: '',
          status: 429,
          statusText: 'Too Many Requests',
          headers,
        }
      )
    }
    return {
      config,
      status: 200,
      statusText: 'OK',
      headers,
      data: {
        success: true,
        data: isTemplate ? { template: {}, rule: {}, ready: false } : {},
      },
    }
  }

  const client = new QueryClient()
  const user = userEvent.setup()
  const view = render(
    <QueryClientProvider client={client}>
      <HealthRules
        selection={{
          channel_id: 1,
          model: 'MiniMax-H3',
          endpoint: 'openai-video',
        }}
      />
    </QueryClientProvider>
  )
  try {
    expect(await screen.findByText('Too many requests')).toBeVisible()
    expect(screen.getByText('Please try again later')).toBeVisible()
    expect(screen.queryByText('Monitoring budget')).not.toBeInTheDocument()
    templateLimited = false
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    expect(
      await screen.findByRole('heading', { name: 'Provider probe template' })
    ).toBeVisible()
    expect(screen.queryByText('Too many requests')).not.toBeInTheDocument()
  } finally {
    view.unmount()
    client.clear()
  }
})

it('offers only whole-day paid media intervals while leaving text cadence editable', async () => {
  api.defaults.adapter = async (config) => ({
    config,
    status: 200,
    statusText: 'OK',
    headers: {},
    data: {
      success: true,
      data: {
        interval_seconds: config.params?.scope === 'video' ? 604800 : 86400,
        per_cycle: 1,
        per_day: 1,
      },
    },
  })
  const client = new QueryClient()
  const user = userEvent.setup()
  const view = render(
    <QueryClientProvider client={client}>
      <HealthRules />
    </QueryClientProvider>
  )
  try {
    await user.selectOptions(screen.getByLabelText('Rule scope'), 'video')
    const video = await screen.findByRole('spinbutton', {
      name: 'Detection interval',
    })
    expect(video).toHaveValue(7)
    expect(video).toHaveAttribute('min', '4')
    expect(video).toHaveAttribute('max', '7')
    expect(screen.getByLabelText('Cycle unit')).toBeDisabled()
    expect(screen.getByLabelText('Runs per cycle')).toHaveAttribute('readonly')
    await user.selectOptions(screen.getByLabelText('Rule scope'), 'llm')
    expect(
      await screen.findByRole('spinbutton', { name: 'Detection interval' })
    ).toHaveValue(1)
    expect(screen.getByLabelText('Cycle unit')).toBeEnabled()
  } finally {
    view.unmount()
    client.clear()
  }
})

it('saves automatic templates separately without enabling scheduled detection', async () => {
  let saved: Record<string, unknown> | undefined
  api.defaults.adapter = async (config) => {
    const automatic = config.url?.endsWith('/auto-templates')
    if (config.method === 'put') {
      expect(automatic).toBe(true)
      saved = JSON.parse(config.data as string)
    }
    return {
      config,
      status: 200,
      statusText: 'OK',
      headers: {},
      data: {
        success: true,
        data: automatic
          ? {
              enabled: false,
              credential: 'MODEL_HEALTH_KEY_PLATFORM',
              token_id: 42,
              max_micro: 10000,
            }
          : {
              enabled: false,
              interval_seconds: 86400,
              per_cycle: 1,
              per_day: 1,
            },
      },
    }
  }
  const client = new QueryClient()
  const user = userEvent.setup()
  const view = render(
    <QueryClientProvider client={client}>
      <HealthRules />
    </QueryClientProvider>
  )
  try {
    const automatic = within(
      await screen.findByRole('region', { name: 'Automatic probe templates' })
    )
    await user.click(
      await automatic.findByRole('checkbox', {
        name: 'Generate probe templates automatically',
      })
    )
    await user.click(automatic.getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(saved).toEqual({
        enabled: true,
        credential: 'MODEL_HEALTH_KEY_PLATFORM',
        token_id: 42,
        max_micro: 10000,
      })
    )
    expect(
      screen.getByRole('checkbox', { name: 'Enable scheduled detection' })
    ).not.toBeChecked()
  } finally {
    view.unmount()
    client.clear()
  }
})
