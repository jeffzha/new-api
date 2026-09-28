/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { Skeleton } from '@/components/ui/skeleton'
import { formatNumber, formatQuota } from '@/lib/format'

import type { QuotaDataItem } from '../../types'

export function ConsoleModelBreakdown(props: {
  data: QuotaDataItem[]
  loading: boolean
}) {
  const { t } = useTranslation()
  const rows = useMemo(() => {
    const models = new Map<
      string,
      { name: string; requests: number; tokens: number; quota: number }
    >()
    for (const row of props.data) {
      const name = row.model_name || 'Unknown'
      const model = models.get(name) ?? {
        name,
        requests: 0,
        tokens: 0,
        quota: 0,
      }
      model.requests += row.count ?? 0
      model.tokens += row.token_used ?? 0
      model.quota += row.quota ?? 0
      models.set(name, model)
    }
    return [...models.values()].sort((a, b) => b.quota - a.quota)
  }, [props.data])
  const total = rows.reduce((value, row) => value + row.quota, 0)
  return (
    <section
      className='console-model-breakdown'
      aria-label={t('Model cost breakdown')}
    >
      <div className='console-resource-heading'>
        <h3>{t('Model cost breakdown')}</h3>
        <span>
          {t('Models')}: {rows.length}
        </span>
      </div>
      {props.loading ? (
        <Skeleton className='h-56 w-full' />
      ) : (
        <StaticDataTable
          tableProps={{ 'aria-label': t('Model cost breakdown') }}
          data={rows}
          getRowKey={(row) => row.name}
          emptyContent={t('No data')}
          columns={[
            {
              id: 'model',
              header: t('Model'),
              cell: (row) => (
                <span
                  className='block max-w-56 truncate font-medium'
                  title={row.name}
                >
                  {row.name === 'Unknown' ? t('Unknown') : row.name}
                </span>
              ),
            },
            {
              id: 'requests',
              header: t('Requests'),
              className: 'text-right',
              cellClassName: 'text-right tabular-nums',
              cell: (row) => formatNumber(row.requests),
            },
            {
              id: 'tokens',
              header: t('Tokens'),
              className: 'text-right',
              cellClassName: 'text-right tabular-nums',
              cell: (row) => formatNumber(row.tokens),
            },
            {
              id: 'quota',
              header: t('Usage'),
              className: 'text-right',
              cellClassName: 'text-right tabular-nums',
              cell: (row) => formatQuota(row.quota),
            },
            {
              id: 'share',
              header: t('Cost share'),
              className: 'text-right',
              cell: (row) => (
                <div className='console-cost-share'>
                  <meter
                    min={0}
                    max={100}
                    value={total > 0 ? (row.quota / total) * 100 : 0}
                    aria-label={`${row.name} ${t('Cost share')}`}
                  />
                  <span>
                    {total > 0 ? ((row.quota / total) * 100).toFixed(1) : '0'}%
                  </span>
                </div>
              ),
            },
          ]}
        />
      )}
    </section>
  )
}
