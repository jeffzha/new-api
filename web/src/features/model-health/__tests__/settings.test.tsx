import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AxiosError, AxiosHeaders } from 'axios'
import { afterEach, expect, it } from 'vitest'

import { api } from '@/lib/http-client'

import { HealthCycleBudget } from '../budget-settings'
import { HealthNotifications, HealthRules } from '../settings'

const originalAdapter = api.defaults.adapter
const cycleStatus = {
  scope: 'global',
  override: true,
  configured: true,
  source: 'global',
  amount_micro: 200000000,
  run_micro: 5000000,
  interval_seconds: 604800,
  next_interval_seconds: 0,
  start: 1795622400,
  end: 1796227200,
  used_micro: 190000000,
  reserved_micro: 10000000,
  remaining_micro: 10000000,
  global_remaining_micro: 10000000,
  recommended_micro: 200000000,
  recommended_run_micro: 5000000,
  estimated_round_micro: 60000000,
  targets: 16,
  missing_templates: 0,
  over_run: 1,
  warnings: ['Remaining cycle budget may not cover all scheduled detections'],
}

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
    let data: object = {}
    if (isTemplate) data = { template: {}, rule: {}, ready: false }
    if (config.url?.endsWith('/cycle-budget')) data = cycleStatus
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
        data,
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
      data: config.url?.endsWith('/cycle-budget')
        ? cycleStatus
        : {
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
    let data: object = {
      enabled: false,
      interval_seconds: 86400,
      per_cycle: 1,
      per_day: 1,
    }
    if (automatic) {
      data = {
        enabled: false,
        credential: 'MODEL_HEALTH_KEY_PLATFORM',
        token_id: 42,
        max_micro: 10000,
      }
    }
    if (config.url?.endsWith('/cycle-budget')) data = cycleStatus
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
        data,
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

it('shows insufficient budget and saves CNY amounts with the chosen cycle without a monthly field', async () => {
  let saved: Record<string, unknown> | undefined
  api.defaults.adapter = async (config) => {
    if (config.method === 'put') saved = JSON.parse(config.data as string)
    return {
      config,
      status: 200,
      statusText: 'OK',
      headers: {},
      data: { success: true, data: cycleStatus },
    }
  }
  const client = new QueryClient()
  const user = userEvent.setup()
  const view = render(
    <QueryClientProvider client={client}>
      <HealthCycleBudget scope='global' />
    </QueryClientProvider>
  )
  try {
    expect(
      await screen.findByText(
        'Remaining cycle budget may not cover all scheduled detections'
      )
    ).toBeVisible()
    expect(screen.queryByLabelText(/month/i)).not.toBeInTheDocument()
    const amount = screen.getByLabelText('Cycle budget (CNY)')
    await user.clear(amount)
    await user.type(amount, '180.25')
    await user.click(screen.getByRole('button', { name: 'Save cycle budget' }))
    await waitFor(() =>
      expect(saved).toEqual({
        scope: 'global',
        override: true,
        amount_micro: 180250000,
        run_micro: 5000000,
        interval_seconds: 604800,
      })
    )
  } finally {
    view.unmount()
    client.clear()
  }
})

it('inherits the type budget until a model override is enabled and rejects a run limit over five yuan', async () => {
  let saved = false
  api.defaults.adapter = async (config) => {
    if (config.method === 'put') saved = true
    return {
      config,
      status: 200,
      statusText: 'OK',
      headers: {},
      data: {
        success: true,
        data: {
          ...cycleStatus,
          scope: 'model',
          source: 'video',
          override: false,
        },
      },
    }
  }
  const client = new QueryClient()
  const user = userEvent.setup()
  const view = render(
    <QueryClientProvider client={client}>
      <HealthCycleBudget
        scope='model'
        selection={{
          channel_id: 1,
          model: 'example-video',
          endpoint: 'openai-video',
        }}
      />
    </QueryClientProvider>
  )
  try {
    expect(await screen.findByLabelText('Cycle budget (CNY)')).toBeDisabled()
    await user.click(
      screen.getByRole('checkbox', {
        name: 'Set a separate budget for this scope',
      })
    )
    expect(screen.getByLabelText('Cycle budget (CNY)')).toBeEnabled()
    const run = screen.getByLabelText('Per-detection limit (CNY)')
    await user.clear(run)
    await user.type(run, '6')
    await user.click(screen.getByRole('button', { name: 'Save cycle budget' }))
    expect(saved).toBe(false)
    expect(run).toBeInvalid()
  } finally {
    view.unmount()
    client.clear()
  }
})
