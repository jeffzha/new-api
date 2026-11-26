import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, within, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/http-client'

import type { Overview } from '../api'
import { HealthOverview } from '../overview'

const originalAdapter = api.defaults.adapter
const clients: QueryClient[] = []

afterEach(() => {
  api.defaults.adapter = originalAdapter
  for (const client of clients.splice(0)) client.clear()
})

it.each([
  ['missing', 'Template required'],
  ['draft', 'Template needs confirmation'],
] as const)(
  'shows a %s video template as waiting and prevents submission',
  async (templateState, label) => {
    const data: Overview = {
      rows: [
        {
          id: 'video-4',
          channel_id: 4,
          channel: 'Fixture provider',
          model: 'Video model',
          upstream: 'upstream-video',
          type: 'video',
          groups: ['default'],
          state: 'waiting',
          paused: false,
          details: [
            {
              name: 'openai-video',
              state: 'waiting',
              stale: false,
              configured: false,
              template_state: templateState,
              next_at: 0,
              last_user: 0,
              last_probe: 0,
              last_probe_source: '',
              user_count: 0,
              user_ok: 0,
              probe_count: 0,
              probe_ok: 0,
              excluded: 0,
              recent: [],
            },
          ],
        },
      ],
      total: 1,
      counts: { waiting: 1 },
    }
    api.defaults.adapter = async (config) => ({
      data: {
        success: true,
        data: config.url?.endsWith('/plan')
          ? { template: {}, ready: false, blocked: ['Template required'] }
          : data,
      },
      status: 200,
      statusText: 'OK',
      headers: {},
      config,
    })
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    clients.push(client)
    const user = userEvent.setup()
    render(
      <QueryClientProvider client={client}>
        <HealthOverview
          access={{
            probe: true,
            rules: true,
            notify: true,
            export: true,
            worker: true,
          }}
          onRules={vi.fn()}
        />
      </QueryClientProvider>
    )

    const row = await screen.findByRole('row', { name: /Video model/ })
    const distribution = screen.getByRole('region', {
      name: 'Health distribution',
    })
    expect(within(distribution).getByText('waiting')).toBeVisible()
    expect(within(distribution).getAllByText('1')).toHaveLength(1)
    const outcomes = screen.getByRole('region', { name: 'Call outcomes' })
    expect(within(outcomes).getByText('No completed samples')).toBeVisible()
    expect(within(row).getByText('waiting')).toBeVisible()
    expect(within(row).queryByText('upstream-video')).not.toBeInTheDocument()
    expect(screen.getAllByRole('columnheader').at(-1)).toHaveTextContent(
      'Actions'
    )
    await user.click(within(row).getByRole('button', { name: /openai-video/ }))
    expect(await screen.findByText(label)).toBeVisible()
    await user.keyboard('{Escape}')
    await user.click(within(row).getByRole('button', { name: 'Run detection' }))

    const details = await screen.findByRole('alertdialog')
    expect(await within(details).findByText('Template required')).toBeVisible()
    expect(
      within(details).getByRole('button', { name: 'Run detection' })
    ).toBeDisabled()
  }
)

it('keeps all matching outcome totals unchanged across pages and page sizes', async () => {
  api.defaults.adapter = async (config) => {
    const empty = config.params.q === 'missing-model'
    return {
      config,
      status: 200,
      statusText: 'OK',
      headers: {},
      data: {
        success: true,
        data: empty
          ? {
              total: 0,
              counts: {},
              rows: [],
              outcomes: {
                user_count: 0,
                user_ok: 0,
                probe_count: 0,
                probe_ok: 0,
              },
            }
          : {
              total: 25,
              counts: { healthy: 25 },
              outcomes: {
                user_count: 106,
                user_ok: 104,
                probe_count: 33,
                probe_ok: 32,
              },
              rows: [
                {
                  id: 'text-1',
                  model: 'Text fixture',
                  channel: 'Fixture',
                  details: [
                    {
                      name: 'openai',
                      state: 'healthy',
                      last_probe: 0,
                      last_user: 0,
                      recent: [],
                      user_count: 6,
                      user_ok: 4,
                      probe_count: 3,
                      probe_ok: 2,
                    },
                  ],
                },
              ],
            },
      },
    }
  }
  const client = new QueryClient()
  clients.push(client)
  const user = userEvent.setup()
  render(
    <QueryClientProvider client={client}>
      <HealthOverview
        access={{
          probe: false,
          rules: false,
          notify: false,
          export: false,
          worker: false,
        }}
        onRules={vi.fn()}
      />
    </QueryClientProvider>
  )
  const outcomes = within(
    await screen.findByRole('region', { name: 'Call outcomes' })
  )
  expect(
    outcomes.getByText('succeeded: 104 · Failed or unconfirmed: 2')
  ).toBeVisible()
  expect(
    outcomes.getByText('succeeded: 32 · Failed or unconfirmed: 1')
  ).toBeVisible()
  await user.click(screen.getByRole('button', { name: 'Go to next page' }))
  expect(
    await screen.findByText('succeeded: 104 · Failed or unconfirmed: 2')
  ).toBeVisible()
  expect(screen.getByPlaceholderText('Channel or model')).toHaveAccessibleName(
    'Channel or model'
  )
  expect(screen.getByRole('region', { name: 'Monitored models' })).toHaveClass(
    'overflow-hidden'
  )
  await user.click(screen.getByRole('combobox', { name: '' }))
  await user.click(await screen.findByRole('option', { name: /^50$/ }))
  await waitFor(() =>
    expect(
      screen.getByText('succeeded: 104 · Failed or unconfirmed: 2')
    ).toBeVisible()
  )
  await user.type(screen.getByLabelText('Channel or model'), 'missing-model')
  await user.click(screen.getByRole('button', { name: 'Search' }))
  expect(
    await screen.findByText('No matching models', { selector: 'p' })
  ).toBeVisible()
  expect(screen.getByText('No completed samples')).toBeVisible()
  expect(screen.queryByText('Text fixture')).not.toBeInTheDocument()
})
