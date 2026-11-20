/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { formatBillingCurrencyFromUSD } from '@/lib/currency'
import { useSystemConfigStore } from '@/stores/system-config-store'

import type { PricingModel, TokenUnit } from '../types'
import { getOfficialRows } from './official-price-data'
import type { ListPriceRow } from './types'

const priceFields = [
  ['input_per_million', 'Input', 'tokens'],
  ['output_per_million', 'Output', 'tokens'],
  ['audio_input_per_million', 'Audio Input', 'tokens'],
  ['audio_output_per_million', 'Audio Output', 'tokens'],
  ['per_image', 'Image generation unit price', 'image'],
  ['per_second', 'Duration unit price', 'second'],
  ['per_million_tokens', 'Video token unit price', 'tokens'],
  ['per_output_credit', 'Output credit unit price', 'credit'],
  ['per_material_credit', 'Material credit unit price', 'credit'],
  ['per_reference_image', 'Reference image unit price', 'image'],
  ['per_reference_video_second', 'Reference video unit price', 'second'],
  ['cache_read_per_million', 'Cache Read', 'tokens'],
  ['cache_write_per_million', 'Cache Write', 'tokens'],
  ['cache_write_1h_per_million', 'Cache Write (1h)', 'tokens'],
  ['per_request', 'Per-request', 'request'],
] as const

// OPS rows are already validated, inherited and sorted by the backend. Only
// display values are converted here. Optional recharge display uses the public
// recharge rate; customer and group sales coefficients never apply.
export function OfficialPrice(props: {
  model: PricingModel
  tokenUnit?: TokenUnit
  usdExchangeRate: number
  showRechargePrice?: boolean
  priceRate?: number
  variant?: 'summary' | 'details' | 'cache'
}) {
  const { t } = useTranslation()
  useSystemConfigStore((state) => state.config.currency)
  const rows = getOfficialRows(props.model)
  if (!rows) return null
  const sourceRate =
    props.model.list_price?.currency === 'USD' ? 1 : props.usdExchangeRate
  const tokenUnit = props.tokenUnit === 'K' ? '1K' : '1M'
  const tokenScale = props.tokenUnit === 'K' ? 1000 : 1
  const format = (field: (typeof priceFields)[number], values: number[]) => {
    const scale = field[2] === 'tokens' ? tokenScale : 1
    const recharge = props.showRechargePrice
      ? (props.priceRate ?? 1) / props.usdExchangeRate
      : 1
    const amounts = values.map((value) =>
      formatBillingCurrencyFromUSD((value / sourceRate / scale) * recharge, {
        digitsLarge: 6,
        digitsSmall: 6,
        abbreviate: false,
      })
    )
    const amount =
      amounts[0] === amounts.at(-1)
        ? amounts[0]
        : `${amounts[0]}–${amounts.at(-1)}`
    return amount
  }
  const unit = (field: (typeof priceFields)[number]) => {
    let unit = t(field[2])
    if (field[2] === 'tokens') unit = t('{{unit}} tokens', { unit: tokenUnit })
    if (field[2] === 'credit') {
      unit = t('pricing credit', { defaultValue: 'credit' })
    }
    return unit
  }
  const contextOnly =
    rows.length > 1 &&
    rows.every((row) =>
      Object.keys(row.when).every((key) => key === 'inputMax')
    )
  const limits = rows
    .flatMap((row) =>
      typeof row.when.inputMax === 'number' ? [row.when.inputMax] : []
    )
    .sort((a, b) => a - b)
  const condition = (row: ListPriceRow) => {
    if (!Object.keys(row.when).length) {
      if (contextOnly) {
        return t('Input context > {{count}} tokens', { count: limits.at(-1) })
      }
      return rows.length > 1 ? t('Other conditions') : t('Default')
    }
    return Object.entries(row.when)
      .map(([key, value]) => {
        if (key === 'inputMax') {
          const index = limits.indexOf(Number(value))
          if (contextOnly && index > 0) {
            return t('Input context: {{lower}} < tokens ≤ {{upper}}', {
              lower: limits[index - 1],
              upper: value,
            })
          }
          return t('Input context ≤ {{count}} tokens', { count: Number(value) })
        }
        if (key === 'referenceVideo') {
          return value ? t('With input video') : t('Without input video')
        }
        if (key === 'operation') {
          if (value === 'edit') return t('Video editing')
          if (value === 'video') return t('Video generation')
          return `${t('Operation')}: ${value}`
        }
        if (key === 'period') {
          if (value === 'off_peak') return t('Off-peak period')
          if (value === 'peak') return t('Peak period')
          return `${t('Time period')}: ${value}`
        }
        const labels: Record<string, string> = {
          resolution: 'Resolution',
          size: 'Size',
          quality: 'Quality',
        }
        return `${t(labels[key] ?? key)}: ${value}`
      })
      .join(' · ')
  }
  const fields = priceFields.filter(([key]) =>
    rows.some((row) => key in row.prices)
  )
  if (props.variant === 'details') {
    return (
      <div className='flex min-w-0 flex-col gap-3'>
        <p className='text-muted-foreground text-xs'>
          {t('Operations list prices before discounts')}
        </p>
        {rows.length === 1 && !Object.keys(rows[0].when).length ? (
          <div
            role='group'
            aria-label={t('Operations price details')}
            className='grid grid-cols-1 gap-2 sm:grid-cols-2'
          >
            {fields.map((field) => (
              <div
                key={field[0]}
                className='bg-muted/20 min-w-0 rounded-lg border p-3'
              >
                <div className='text-muted-foreground text-xs'>
                  {t(field[1])}
                </div>
                <div className='text-foreground mt-1 font-mono text-base font-semibold break-words tabular-nums'>
                  {format(field, [Number(rows[0].prices[field[0]])])} /{' '}
                  {unit(field)}
                </div>
              </div>
            ))}
          </div>
        ) : (
          <StaticDataTable
            // The console theme clips table frames. This full-height detail
            // matrix scrolls horizontally without nesting another vertical scroller.
            className='focus-visible:outline-ring overflow-x-auto! rounded-lg focus-visible:outline-2 focus-visible:outline-offset-2'
            containerProps={{
              role: 'region',
              'aria-label': t('Operations price details'),
              tabIndex: 0,
            }}
            tableProps={{
              'aria-label': t('Operations price details'),
              withContainer: false,
            }}
            tableClassName='w-full text-sm'
            headerRowClassName='bg-muted/30 hover:bg-muted/30'
            data={rows}
            getRowKey={(_, index) => index}
            columns={[
              {
                id: 'conditions',
                header: t('Pricing conditions'),
                className: 'text-muted-foreground min-w-44 px-3 py-3',
                cellClassName:
                  'max-w-64 px-3 py-3 whitespace-normal break-words',
                cell: (row) => (
                  <div className='break-words whitespace-normal'>
                    <span>{condition(row)}</span>
                  </div>
                ),
              },
              ...fields.map((field) => ({
                id: field[0],
                header: (
                  <div className='flex flex-col gap-1'>
                    <span>{t(field[1])}</span>{' '}
                    <span data-table-text='secondary'>/ {unit(field)}</span>
                  </div>
                ),
                className:
                  'text-muted-foreground min-w-28 px-3 py-3 text-right',
                cellClassName: 'px-3 py-3 text-right font-mono tabular-nums',
                cell: (row: ListPriceRow) => (
                  <span>
                    {field[0] in row.prices
                      ? format(field, [Number(row.prices[field[0]])])
                      : '—'}
                  </span>
                ),
              })),
            ]}
          />
        )}
        {rows.length > 1 && (
          <p className='text-muted-foreground text-xs'>
            {t(
              'The first matching condition applies; other conditions use the default prices.'
            )}
          </p>
        )}
        {rows.some((row) => 'period' in row.when) && (
          <p className='text-muted-foreground text-xs'>
            {t('Period times follow the operations platform configuration.')}
          </p>
        )}
      </div>
    )
  }
  const visible =
    props.variant === 'cache'
      ? fields.filter(
          ([key]) =>
            key === 'cache_read_per_million' ||
            key === 'cache_write_per_million' ||
            key === 'cache_write_1h_per_million'
        )
      : fields.filter(([key]) => !key.startsWith('cache_')).slice(0, 3)
  if (!visible.length) return <span className='text-muted-foreground'>—</span>
  return (
    <div className='flex w-full min-w-0 flex-wrap items-baseline gap-x-3 gap-y-1'>
      {visible.map((field) => {
        const values = rows
          .flatMap((row) =>
            field[0] in row.prices ? [Number(row.prices[field[0]])] : []
          )
          .sort((a, b) => a - b)
        return (
          <div
            key={field[0]}
            className='inline-flex min-w-0 flex-wrap items-baseline gap-x-1.5'
          >
            <span className='text-muted-foreground shrink-0 text-xs'>
              {t(field[1])}
            </span>
            <span className='inline-flex items-baseline gap-x-1 whitespace-nowrap'>
              <span className='text-foreground font-mono text-sm font-semibold tabular-nums'>
                {format(field, values)}
              </span>{' '}
              <span className='text-muted-foreground text-xs'>
                / {unit(field)}
              </span>
            </span>
          </div>
        )
      })}
      {rows.length > 1 && (
        <span className='text-muted-foreground basis-full text-xs'>
          {t('Price range; see details for conditions')}
        </span>
      )}
    </div>
  )
}
