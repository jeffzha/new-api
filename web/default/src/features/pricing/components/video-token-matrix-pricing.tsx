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
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { cn } from '@/lib/utils'

import {
  formatProviderPrice,
  scaleVideoTokenPricingTiers,
} from '../lib/provider-pricing'
import type { VideoTokenMatrixPricing } from '../types'

interface VideoTokenMatrixPricingProps {
  pricing: VideoTokenMatrixPricing
  groupRatio?: number
  variant?: 'summary' | 'table'
  maxTiers?: number
  className?: string
}

export function VideoTokenMatrixPricing(props: VideoTokenMatrixPricingProps) {
  const { t } = useTranslation()
  const tiers = scaleVideoTokenPricingTiers(
    props.pricing,
    props.groupRatio ?? 1
  )

  if (props.variant !== 'table') {
    const visibleTiers = props.maxTiers ? tiers.slice(0, props.maxTiers) : tiers
    const hiddenTierCount = tiers.length - visibleTiers.length
    return (
      <div className={cn('min-w-0', props.className)}>
        <div className='space-y-0.5'>
          {visibleTiers.map((tier) => (
            <div
              key={tier.resolution}
              className='grid grid-cols-[3.5rem_minmax(0,1fr)] items-baseline gap-2 text-xs'
            >
              <span className='text-muted-foreground font-medium'>
                {tier.resolution}
              </span>
              <span className='text-foreground truncate font-mono font-semibold tabular-nums'>
                {formatProviderPrice(
                  tier.without_video,
                  props.pricing.currency
                )}
                <span className='text-muted-foreground/40 mx-1'>/</span>
                {formatProviderPrice(tier.with_video, props.pricing.currency)}
              </span>
            </div>
          ))}
          {hiddenTierCount > 0 && (
            <div className='text-muted-foreground/60 text-[10px]'>
              {t('{{count}} tiers', { count: hiddenTierCount })}
            </div>
          )}
        </div>
        <div className='text-muted-foreground/60 mt-1 truncate text-[10px]'>
          {t('Without input video')} / {t('With input video')} {'\u00b7'}{' '}
          {t('CNY / 1M video tokens')}
        </div>
      </div>
    )
  }

  const thClass =
    'text-muted-foreground py-2 text-[10px] font-medium tracking-wider uppercase'

  return (
    <div className={props.className}>
      <StaticDataTable
        className='rounded-none border-0'
        tableClassName='text-sm'
        headerRowClassName='hover:bg-transparent'
        data={tiers}
        getRowKey={(tier) => tier.resolution}
        columns={[
          {
            id: 'resolution',
            header: t('Resolution'),
            className: thClass,
            cellClassName: 'py-2.5 font-medium',
            cell: (tier) => tier.resolution,
          },
          {
            id: 'without-video',
            header: t('Without input video'),
            className: `${thClass} text-right`,
            cellClassName: 'py-2.5 text-right font-mono tabular-nums',
            cell: (tier) =>
              formatProviderPrice(tier.without_video, props.pricing.currency),
          },
          {
            id: 'with-video',
            header: t('With input video'),
            className: `${thClass} text-right`,
            cellClassName: 'py-2.5 text-right font-mono tabular-nums',
            cell: (tier) =>
              formatProviderPrice(tier.with_video, props.pricing.currency),
          },
        ]}
      />
      <p className='text-muted-foreground/50 mt-1.5 text-[10px]'>
        {t('Prices shown per')} {t('CNY / 1M video tokens')}
      </p>
    </div>
  )
}
