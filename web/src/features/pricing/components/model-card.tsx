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
import { ChevronRight } from 'lucide-react'
import { memo, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { Button } from '@/components/ui/button'
import { getLobeIcon } from '@/lib/lobe-icon'
import { cn } from '@/lib/utils'

import { DEFAULT_TOKEN_UNIT } from '../constants'
import { useBillingTime } from '../hooks/use-billing-time'
import {
  getDynamicDisplayGroupRatio,
  getDynamicPricingSummary,
  getDynamicPriceUnitLabelKey,
  getCardExamplePrice,
  getSpecialExpressionPriceNote,
  isUnconfiguredTaskUsageModel,
} from '../lib/dynamic-price'
import { parseTags } from '../lib/filters'
import { getDisplayGroupRatio, isTokenBasedModel } from '../lib/model-helpers'
import { formatPrice, formatRequestPrice } from '../lib/price'
import { getVideoTokenMatrixPricing } from '../lib/provider-pricing'
import { taskUsageUnitLabel } from '../lib/task-price-display'
import type { PricingModel, TokenUnit } from '../types'
import { ModelBillingModeBadge } from './model-billing-mode-badge'
import { ModelPerfBadge, type ModelPerfBadgeData } from './model-perf-badge'
import { VideoTokenMatrixPricing } from './video-token-matrix-pricing'

export interface ModelCardProps {
  model: PricingModel
  onClick: () => void
  priceRate?: number
  usdExchangeRate?: number
  tokenUnit?: TokenUnit
  showRechargePrice?: boolean
  selectedGroup?: string
  perf?: ModelPerfBadgeData
}

export const ModelCard = memo(function ModelCard(props: ModelCardProps) {
  const { t, i18n } = useTranslation()
  const billingTime = useBillingTime(props.model.billing_expr)
  const tokenUnit = props.tokenUnit ?? DEFAULT_TOKEN_UNIT
  const priceRate = props.priceRate ?? 1
  const usdExchangeRate = props.usdExchangeRate ?? 1
  const showRechargePrice = props.showRechargePrice ?? false
  const isTokenBased = isTokenBasedModel(props.model)
  const tokenUnitLabel = tokenUnit === 'K' ? '1K' : '1M'
  const tags = parseTags(props.model.tags)
  const groups = props.model.enable_groups || []
  const endpoints = props.model.supported_endpoint_types || []
  const modelIconKey = props.model.icon || props.model.vendor_icon
  const modelIcon = modelIconKey ? getLobeIcon(modelIconKey, 28) : null
  const initial = props.model.model_name?.charAt(0).toUpperCase() || '?'
  const providerPricing = getVideoTokenMatrixPricing(props.model)
  const hasCachedPrice = isTokenBased && props.model.cache_ratio != null
  const priceOptions = {
    tokenUnit,
    showRechargePrice,
    priceRate,
    usdExchangeRate,
    now: billingTime === undefined ? undefined : new Date(billingTime),
    groupRatioMultiplier: getDynamicDisplayGroupRatio(
      props.model,
      props.selectedGroup
    ),
  }
  const dynamicSummary = getDynamicPricingSummary(props.model, priceOptions)
  const examplePrice = getCardExamplePrice(props.model, priceOptions)

  const primaryGroup =
    props.selectedGroup && groups.includes(props.selectedGroup)
      ? props.selectedGroup
      : groups[0]

  let priceSummary: ReactNode
  if (providerPricing) {
    priceSummary = (
      <VideoTokenMatrixPricing
        pricing={providerPricing}
        groupRatio={getDisplayGroupRatio(props.model, props.selectedGroup)}
        maxTiers={1}
      />
    )
  } else if (dynamicSummary) {
    if (dynamicSummary.isSpecialExpression) {
      const priceNote = getSpecialExpressionPriceNote(props.model)
      priceSummary = priceNote ? (
        <span className='text-muted-foreground text-xs break-words whitespace-normal'>
          {t(priceNote)}
        </span>
      ) : (
        <span className='min-w-0'>
          <span className='text-amber-700 dark:text-amber-300'>
            {t('Special billing expression')}
          </span>
          <code className='text-muted-foreground/70 mt-0.5 line-clamp-1 block font-mono text-[11px] break-all'>
            {dynamicSummary.rawExpression}
          </code>
        </span>
      )
    } else if (dynamicSummary.primaryEntries.length > 0) {
      priceSummary = (
        <>
          {dynamicSummary.primaryEntries.map((entry) => {
            const key = getDynamicPriceUnitLabelKey(entry)
            const unit = taskUsageUnitLabel(
              entry,
              i18n.language,
              key ? t(key) : tokenUnitLabel
            )
            return (
              <span
                key={entry.key}
                className='text-muted-foreground whitespace-nowrap'
              >
                <span>
                  {entry.labelKind === 'schema'
                    ? entry.shortLabel
                    : t(entry.shortLabel)}
                </span>{' '}
                <span className='text-foreground font-mono font-semibold'>
                  {entry.formattedRange ?? entry.formatted}
                </span>{' '}
                <span className='text-xs'>/ {unit}</span>
              </span>
            )
          })}
          {dynamicSummary.isTimePricing && (
            <span className='text-muted-foreground basis-full text-xs'>
              {t('Current period price')}
            </span>
          )}
          {examplePrice && (
            <span className='text-muted-foreground basis-full text-xs'>
              {examplePrice.label} ≈ {examplePrice.formatted}
            </span>
          )}
        </>
      )
    } else {
      priceSummary = (
        <span className='text-muted-foreground text-sm'>
          {t('Dynamic Pricing')}
        </span>
      )
    }
  } else if (isUnconfiguredTaskUsageModel(props.model)) {
    priceSummary = (
      <span className='text-muted-foreground text-sm'>
        {t('Usage-based billing · price not configured')}
      </span>
    )
  } else if (isTokenBased) {
    priceSummary = (
      <>
        <span className='text-muted-foreground whitespace-nowrap'>
          <span>{t('Input')}</span>{' '}
          <span className='text-foreground font-mono font-semibold'>
            {formatPrice(
              props.model,
              'input',
              tokenUnit,
              showRechargePrice,
              priceRate,
              usdExchangeRate,
              props.selectedGroup
            )}
          </span>{' '}
          <span className='text-xs'>/ {tokenUnitLabel}</span>
        </span>
        <span className='text-muted-foreground whitespace-nowrap'>
          <span>{t('Output')}</span>{' '}
          <span className='text-foreground font-mono font-semibold'>
            {formatPrice(
              props.model,
              'output',
              tokenUnit,
              showRechargePrice,
              priceRate,
              usdExchangeRate,
              props.selectedGroup
            )}
          </span>{' '}
          <span className='text-xs'>/ {tokenUnitLabel}</span>
        </span>
        {hasCachedPrice && (
          <span className='text-muted-foreground whitespace-nowrap'>
            <span>{t('Cached')}</span>{' '}
            <span className='text-foreground font-mono font-semibold'>
              {formatPrice(
                props.model,
                'cache',
                tokenUnit,
                showRechargePrice,
                priceRate,
                usdExchangeRate,
                props.selectedGroup
              )}
            </span>{' '}
            <span className='text-xs'>/ {tokenUnitLabel}</span>
          </span>
        )}
      </>
    )
  } else {
    priceSummary = (
      <span className='text-foreground font-mono font-semibold whitespace-nowrap'>
        {formatRequestPrice(
          props.model,
          showRechargePrice,
          priceRate,
          usdExchangeRate,
          props.selectedGroup
        )}{' '}
        / {t('request')}
      </span>
    )
  }

  return (
    <div
      className={cn(
        'signal-model-card group relative flex flex-col rounded-xl border p-3 transition-colors sm:p-5',
        'hover:bg-muted/20'
      )}
    >
      {/* Header: icon + name + price + actions */}
      <div className='flex items-start justify-between gap-2.5 sm:gap-3'>
        <div className='flex min-w-0 items-start gap-2.5 sm:gap-3'>
          <div className='bg-muted/40 flex size-9 shrink-0 items-center justify-center rounded-lg sm:size-10 sm:rounded-xl'>
            {modelIcon || (
              <span className='text-muted-foreground text-sm font-bold'>
                {initial}
              </span>
            )}
          </div>
          <div className='min-w-0'>
            <h3
              title={props.model.model_name}
              className='text-foreground truncate font-mono text-[15px] leading-tight font-bold'
            >
              {props.model.model_name}
            </h3>
            <div className='mt-0.5 flex flex-wrap items-baseline gap-x-2 gap-y-0.5 text-sm sm:mt-1 sm:gap-x-3'>
              {priceSummary}
            </div>
          </div>
        </div>

        <div className='flex shrink-0 items-center gap-1.5'>
          <Button
            type='button'
            onClick={props.onClick}
            variant='outline'
            size='sm'
            className='gap-1'
          >
            {t('Details')}
            <ChevronRight className='size-3.5' />
          </Button>
          <CopyButton
            value={props.model.model_name || ''}
            tooltip={t('Copy model name')}
            variant='outline'
            className='size-8'
          />
        </div>
      </div>

      {/* Description */}
      <p className='text-muted-foreground mt-2 line-clamp-1 flex-1 text-[13px] leading-relaxed sm:mt-4 sm:line-clamp-2 sm:min-h-[2.5rem]'>
        {props.model.description || t('No description available.')}
      </p>

      {/* Footer: left metadata and right performance summary share row alignment */}
      <div className='mt-2 grid grid-cols-[minmax(0,1fr)_auto] items-start gap-x-2 gap-y-1 sm:mt-4'>
        <div className='min-w-0 space-y-2'>
          {primaryGroup && (
            <div className='flex min-w-0 items-center gap-2 text-xs'>
              <span className='text-muted-foreground'>{t('Groups')}</span>
              <span className='truncate' title={primaryGroup}>
                {primaryGroup}
              </span>
              {groups.length > 1 && (
                <span
                  title={groups
                    .filter((group) => group !== primaryGroup)
                    .join(', ')}
                >
                  +{groups.length - 1}
                </span>
              )}
            </div>
          )}
          <div role='group' aria-label={t('Pricing')}>
            <ModelBillingModeBadge model={props.model} />
          </div>
        </div>
        <ModelPerfBadge perf={props.perf} className='row-span-2 self-start' />

        <div className='flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-0.5 sm:gap-x-3 sm:gap-y-1'>
          {endpoints.length > 0 && (
            <div className='flex min-w-0 items-center gap-2 text-xs'>
              <span className='text-muted-foreground'>{t('Endpoints')}</span>
              <span className='truncate' title={endpoints.join(', ')}>
                {endpoints.slice(0, 2).join(', ')}
              </span>
              {endpoints.length > 2 && <span>+{endpoints.length - 2}</span>}
            </div>
          )}
          {tags.length > 0 && (
            <div
              role='group'
              aria-label={t('Tags')}
              className='flex min-w-0 items-center gap-2 text-xs'
            >
              <span className='truncate' title={tags.join(', ')}>
                {tags.slice(0, 2).join(', ')}
              </span>
              {tags.length > 2 && <span>+{tags.length - 2}</span>}
            </div>
          )}
        </div>
      </div>
    </div>
  )
})
