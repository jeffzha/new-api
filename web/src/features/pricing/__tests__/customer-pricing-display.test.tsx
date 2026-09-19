import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it } from 'vitest'

import {
  CustomerPricingNotice,
  PricingUnavailable,
} from '../components/customer-pricing-status'
import { ModelCard } from '../components/model-card'
import { ModelDetailsContent } from '../components/model-details'
import { PricingTable } from '../components/pricing-table'
import type { PricingModel } from '../types'

const customer: PricingModel = {
  id: 1,
  model_name: 'hy3',
  quota_type: 0,
  model_ratio: 2,
  completion_ratio: 3,
  sales_bps: 9000,
  enable_groups: ['default'],
  group_ratio: { default: 2 },
}
const clients: QueryClient[] = []

afterEach(() => {
  clients.forEach((client) => client.clear())
  clients.length = 0
})

function renderDetails(model: PricingModel) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  client.setQueryData(['perf-metrics', model.model_name], {
    success: true,
    data: { groups: [] },
  })
  return render(
    <QueryClientProvider client={client}>
      <ModelDetailsContent
        model={model}
        groupRatio={{ default: 2 }}
        usableGroup={{ default: { desc: 'Default', ratio: 2 } }}
        endpointMap={{}}
        autoGroups={[]}
        priceRate={1}
        usdExchangeRate={1}
        tokenUnit='M'
      />
    </QueryClientProvider>
  )
}

describe('customer pricing display', () => {
  it('opens the agency pricing list from the exclusive-price notice', async () => {
    render(
      <CustomerPricingNotice
        models={[
          customer,
          { ...customer, model_name: 'second-model', sales_bps: 8000 },
          { ...customer, model_name: 'base-model', sales_bps: undefined },
        ]}
      />
    )

    await userEvent.setup().click(
      screen.getByRole('button', { name: 'View agency pricing details' })
    )

    expect(screen.getByRole('dialog')).toHaveTextContent('Agency pricing details')
    expect(screen.getByRole('dialog')).toHaveTextContent('hy3')
    expect(screen.getByRole('dialog')).toHaveTextContent('second-model')
    expect(screen.getByRole('dialog')).not.toHaveTextContent('base-model')
    expect(screen.getByRole('dialog')).toHaveTextContent('90.00%')
    expect(screen.getByRole('dialog')).toHaveTextContent('10.00%')
  })

  it('shows agency input/output prices in both card and table, not ordinary group prices', () => {
    const card = render(
      <ModelCard
        model={customer}
        selectedGroup='default'
        onClick={() => undefined}
      />
    )
    expect(within(card.container).getByText('$3.6')).toBeInTheDocument()
    expect(within(card.container).getByText('$10.8')).toBeInTheDocument()
    expect(within(card.container).queryByText('$8')).not.toBeInTheDocument()
    card.unmount()
    render(<PricingTable models={[customer]} selectedGroup='default' />)
    expect(
      screen.getByRole('cell', { name: /\$3\.6\s*\/\s*\$10\.8/ })
    ).toBeInTheDocument()
    expect(screen.queryByText('$8')).not.toBeInTheDocument()
  })

  it('shows the customer price in detail overview and group price rows', () => {
    renderDetails(customer)
    expect(
      screen.getByRole('heading', { name: 'Your price' })
    ).toBeInTheDocument()
    expect(screen.getAllByText('$3.6')).toHaveLength(2)
    expect(screen.getAllByText('$10.8')).toHaveLength(2)
    expect(screen.getByText('0.9x')).toBeInTheDocument()
    expect(screen.queryByText('2x')).not.toBeInTheDocument()
    expect(screen.getByRole('alert')).toHaveTextContent(
      'Prices include your agency sales policy.'
    )
  })

  it('shows the customer per-call price in card, table, and detail group rows', () => {
    const model = { ...customer, quota_type: 1, model_price: 2 }
    const card = render(
      <ModelCard
        model={model}
        selectedGroup='default'
        onClick={() => undefined}
      />
    )
    expect(within(card.container).getByText('$1.8')).toBeInTheDocument()
    card.unmount()
    const table = render(
      <PricingTable models={[model]} selectedGroup='default' />
    )
    expect(screen.getByRole('cell', { name: /\$1\.8/ })).toBeInTheDocument()
    table.unmount()
    renderDetails(model)
    expect(screen.getAllByText('$1.8')).toHaveLength(2)
    expect(screen.queryByText('$4')).not.toBeInTheDocument()
  })

  it('shows customer video tier amounts in every pricing view without applying USD conversion', () => {
    const model: PricingModel = {
      ...customer,
      provider_pricing: {
        kind: 'video_token_matrix',
        currency: 'CNY',
        unit: '1M_video_tokens',
        tiers: [{ resolution: '720p', without_video: 20, with_video: 10 }],
      },
    }
    const card = render(
      <ModelCard
        model={model}
        selectedGroup='default'
        onClick={() => undefined}
      />
    )
    expect(card.container).toHaveTextContent('¥18/¥9')
    expect(card.container).not.toHaveTextContent('¥40')
    card.unmount()
    const table = render(
      <PricingTable models={[model]} selectedGroup='default' />
    )
    expect(
      screen.getByRole('cell', { name: /¥18\s*\/\s*¥9/ })
    ).toBeInTheDocument()
    table.unmount()
    renderDetails(model)
    expect(screen.getAllByRole('cell', { name: '¥18' })).toHaveLength(2)
    expect(screen.getAllByRole('cell', { name: '¥9' })).toHaveLength(2)
    expect(screen.queryByText('¥40')).not.toBeInTheDocument()
  })

  it('scales every dynamic tier display, including detailed expression breakdown', () => {
    renderDetails({
      ...customer,
      billing_mode: 'tiered_expr',
      billing_expr: 'tier("base", p * 2 + c * 8)',
    })
    expect(screen.getAllByText('$1.8')).toHaveLength(2)
    expect(screen.getAllByText('$7.2')).toHaveLength(2)
    expect(screen.getAllByText('$1.8000').length).toBeGreaterThan(0)
    expect(screen.getAllByText('$7.2000').length).toBeGreaterThan(0)
    expect(screen.queryByText('$2.0000')).not.toBeInTheDocument()
  })

  it('preserves zero-priced customer tiers without falling back to the group ratio', () => {
    renderDetails({
      ...customer,
      sales_bps: 0,
      billing_mode: 'tiered_expr',
      billing_expr: 'tier("base", p * 2 + c * 8)',
    })
    expect(screen.getByText('0x')).toBeInTheDocument()
    expect(screen.getAllByText('$0.0000').length).toBeGreaterThan(0)
    expect(screen.queryByText('$2.0000')).not.toBeInTheDocument()
  })

  it('keeps ordinary base price and group-price separation for an unbound customer', () => {
    renderDetails({ ...customer, sales_bps: undefined })
    expect(
      screen.getByRole('heading', { name: 'Base Price' })
    ).toBeInTheDocument()
    expect(screen.getByText('$4')).toBeInTheDocument()
    expect(screen.getByText('$8')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('allows retry from the unavailable-price state without showing fallback prices', async () => {
    let retried = false
    render(
      <PricingUnavailable
        onRetry={() => {
          retried = true
        }}
      />
    )
    expect(screen.getByRole('alert')).toHaveTextContent(
      'Pricing is temporarily unavailable.'
    )
    await userEvent.setup().click(screen.getByRole('button', { name: 'Retry' }))
    expect(retried).toBe(true)
  })
})
