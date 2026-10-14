/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { cn } from '@/lib/utils'

import {
  formatTaskUsageUnitPrice,
  type DynamicPriceOptions,
} from '../lib/dynamic-price'
import { taskPriceLabel, taskUsageUnitLabel } from '../lib/task-price-display'
import type {
  VideoMaterialFieldRow,
  VideoMaterialPricing,
  VideoPricingTable,
} from '../lib/video-pricing-table'
import type { BillingUsageFieldSchema, TokenUnit } from '../types'

type VideoPricingTablesProps = {
  table: VideoPricingTable
  tokenUnit: TokenUnit
  showRechargePrice?: boolean
  priceRate: number
  usdExchangeRate: number
  groupRatio?: number
  className?: string
}

type MaterialTableRow = {
  pricing: VideoMaterialPricing
  row?: VideoMaterialFieldRow
}

function materialUnitLabel(
  definition: BillingUsageFieldSchema,
  language: string,
  t: (key: string) => string
): string {
  if (definition.unit === 'second') return t('s')
  if (definition.unit === 'credit') return t('credit')
  return taskUsageUnitLabel(definition, language, t('unit'))
}

export function VideoPricingTables(props: VideoPricingTablesProps) {
  const { t, i18n } = useTranslation()

  const priceOptions: DynamicPriceOptions = {
    tokenUnit: props.tokenUnit,
    showRechargePrice: props.showRechargePrice ?? false,
    priceRate: props.priceRate,
    usdExchangeRate: props.usdExchangeRate,
    groupRatioMultiplier: props.groupRatio ?? 1,
  }

  const materialPricing = props.table.materialPricing
  const materialRows: MaterialTableRow[] = useMemo(() => {
    if (!materialPricing) return []
    if (materialPricing.kind === 'usage_fields') {
      return materialPricing.rows.map((row) => ({
        pricing: materialPricing,
        row,
      }))
    }
    return [{ pricing: materialPricing }]
  }, [materialPricing])

  const thClass =
    'text-muted-foreground py-2 text-[10px] font-medium tracking-wider uppercase'
  const cellClass = 'py-2.5 align-top whitespace-normal break-words'

  const materialRuleOf = (row: MaterialTableRow): string => {
    if (row.pricing.kind === 'upstream_credits') {
      return t("Settled according to the supplier's final material charge")
    }
    if (row.row) {
      const rate = formatTaskUsageUnitPrice(row.row.amountUSD, priceOptions)
      return `${rate} / ${materialUnitLabel(row.row.definition, i18n.language, t)}`
    }
    return t('No extra material charge')
  }

  return (
    <div className={cn('space-y-4', props.className)}>
      <div className='space-y-2'>
        <div className='text-sm font-medium'>{t('Video output pricing')}</div>
        <StaticDataTable
          tableClassName='text-sm'
          headerRowClassName='hover:bg-transparent'
          data={props.table.outputRows}
          getRowKey={(row) => row.key}
          columns={[
            {
              id: 'model',
              header: t('Model / API'),
              className: thClass,
              cellClassName: `${cellClass} font-medium`,
              cell: () => props.table.modelLabel,
            },
            {
              id: 'resolution',
              header: t('Resolution'),
              className: thClass,
              cellClassName: `${cellClass} font-medium`,
              cell: (row) => row.resolution,
            },
            {
              id: 'rule',
              header: t('Billing rule'),
              className: thClass,
              cellClassName: `${cellClass} text-muted-foreground`,
              cell: (row) =>
                row.rule === 'second'
                  ? t('Per second')
                  : [
                      t('Per request'),
                      row.seconds
                        ? t('Fixed {{seconds}}s', { seconds: row.seconds })
                        : '',
                    ]
                      .filter(Boolean)
                      .join(' · '),
            },
            {
              id: 'price',
              header: t('Price'),
              className: `${thClass} text-right`,
              cellClassName: `${cellClass} text-right font-mono tabular-nums`,
              cell: (row) => (
                <>
                  {formatTaskUsageUnitPrice(row.amountUSD, priceOptions)}
                  <span className='text-muted-foreground/40 ml-1 text-xs'>
                    / {row.rule === 'second' ? t('s') : t('request')}
                  </span>
                </>
              ),
            },
          ]}
        />
      </div>
      {materialPricing && (
        <div className='space-y-2'>
          <div className='text-sm font-medium'>
            {t('Video material pricing')}
          </div>
          <StaticDataTable
            tableClassName='text-sm'
            headerRowClassName='hover:bg-transparent'
            data={materialRows}
            getRowKey={(row) => row.row?.field ?? 'combined-materials'}
            columns={[
              {
                id: 'model',
                header: t('Model / API'),
                className: thClass,
                cellClassName: `${cellClass} font-medium`,
                cell: () => props.table.modelLabel,
              },
              {
                id: 'material',
                header: t('Material'),
                className: thClass,
                cellClassName: `${cellClass} font-medium`,
                cell: (row) =>
                  row.row
                    ? taskPriceLabel(
                        row.row.definition.description,
                        row.row.field,
                        i18n.language
                      )
                    : t('Reference materials (audio, images and video)'),
              },
              {
                id: 'rule',
                header: t('Billing rule'),
                className: thClass,
                cellClassName: `${cellClass} text-muted-foreground`,
                cell: (row) => materialRuleOf(row),
              },
            ]}
          />
          {materialPricing.kind === 'upstream_credits' && (
            <p className='text-muted-foreground/50 text-[10px]'>
              {t(
                'The supplier returns only the combined material charge, not separate per-image or per-second prices. No material fee is added when the supplier charges none.'
              )}
            </p>
          )}
        </div>
      )}
    </div>
  )
}
