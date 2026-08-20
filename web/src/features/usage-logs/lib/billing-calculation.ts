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
import type { UsageLog } from '../data/schema'
import type { LogOtherData } from '../types'

export type BillingComponentUnit = 'million_tokens' | 'thousand_calls' | 'call'

export type BillingRatioEntry = {
  name: string
  value: number
}

export type BillingComponent = {
  key: string
  labelKey: string
  quantity: number
  unitPriceUSD: number
  unit: BillingComponentUnit
  ratios: BillingRatioEntry[]
  subtotalUSD: number
}

export type BillingCalculation = {
  components: BillingComponent[]
  ratios: BillingRatioEntry[]
  effectiveGroupRatio: number
  otherRatioMultiplier: number
  effectiveMultiplier: number
  calculatedCostUSD: number
  rawQuota: number
  quotaPerUnit: number
  totalInputTokens: number
  billableInputTokens: number
  cacheReadTokens: number
  cacheWriteTokens: number
  cacheWrite5mTokens: number
  cacheWrite1hTokens: number
}

function finiteNumber(value: number | undefined, fallback = 0): number {
  return value != null && Number.isFinite(value) ? value : fallback
}

function positiveNumber(value: number | undefined, fallback = 0): number {
  const normalized = finiteNumber(value, fallback)
  return normalized > 0 ? normalized : fallback
}

function effectiveGroupRatio(other: LogOtherData): number {
  const userRatio = other.user_group_ratio
  if (userRatio != null && Number.isFinite(userRatio) && userRatio !== -1) {
    return userRatio
  }
  return finiteNumber(other.group_ratio, 1)
}

function componentDivisor(unit: BillingComponentUnit): number {
  if (unit === 'million_tokens') return 1_000_000
  if (unit === 'thousand_calls') return 1_000
  return 1
}

export function buildBillingCalculation(
  log: UsageLog,
  other: LogOtherData,
  currentQuotaPerUnit: number
): BillingCalculation {
  const groupRatio = effectiveGroupRatio(other)
  const otherRatios = Object.entries(other.other_ratios || {})
    .filter((entry): entry is [string, number] => {
      return Number.isFinite(entry[1]) && entry[1] > 0
    })
    .sort(([left], [right]) => left.localeCompare(right))
  const otherRatioMultiplier = otherRatios.reduce(
    (product, [, ratio]) => product * ratio,
    1
  )
  const effectiveMultiplier = groupRatio * otherRatioMultiplier
  const quotaPerUnit = positiveNumber(other.quota_per_unit, currentQuotaPerUnit)
  const cacheReadTokens = Math.max(0, finiteNumber(other.cache_tokens))
  const cacheWrite5mTokens = Math.max(
    0,
    finiteNumber(other.cache_creation_tokens_5m)
  )
  const cacheWrite1hTokens = Math.max(
    0,
    finiteNumber(other.cache_creation_tokens_1h)
  )
  const splitCacheWriteTokens = cacheWrite5mTokens + cacheWrite1hTokens
  const reportedCacheWriteTokens = Math.max(
    0,
    finiteNumber(
      other.cache_write_tokens,
      finiteNumber(other.cache_creation_tokens)
    )
  )
  const cacheWriteTokens = Math.max(
    reportedCacheWriteTokens,
    splitCacheWriteTokens
  )
  const genericCacheWriteTokens = Math.max(
    0,
    cacheWriteTokens - splitCacheWriteTokens
  )
  const imageInputTokens = Math.max(0, finiteNumber(other.image_output))
  const audioInputTokens = Math.max(
    0,
    finiteNumber(other.audio_input_token_count, finiteNumber(other.audio_input))
  )
  const isAnthropic =
    other.usage_semantic === 'anthropic' || other.claude === true
  const totalInputTokens = Math.max(
    0,
    finiteNumber(
      other.input_tokens_total,
      isAnthropic
        ? log.prompt_tokens + cacheReadTokens + cacheWriteTokens
        : log.prompt_tokens
    )
  )

  let fallbackBillableInputTokens = log.prompt_tokens
  if (!isAnthropic && !other.ws && !other.audio) {
    fallbackBillableInputTokens -= cacheReadTokens
    fallbackBillableInputTokens -= cacheWriteTokens
    fallbackBillableInputTokens -= imageInputTokens
    if (other.audio_input_seperate_price) {
      fallbackBillableInputTokens -= audioInputTokens
    }
  }
  if (other.ws || other.audio) {
    fallbackBillableInputTokens = finiteNumber(
      other.text_input,
      log.prompt_tokens
    )
  }
  const billableInputTokens = Math.max(
    0,
    finiteNumber(other.billable_input_tokens, fallbackBillableInputTokens)
  )
  const isTiered = other.billing_mode === 'tiered_expr'
  const componentMultiplier = isTiered ? groupRatio : effectiveMultiplier

  const ratios: BillingRatioEntry[] = []
  const addRatio = (name: string, value: number | undefined) => {
    if (value == null || !Number.isFinite(value)) return
    ratios.push({ name, value })
  }
  addRatio('model_ratio', other.model_ratio)
  addRatio('completion_ratio', other.completion_ratio)
  addRatio('cache_ratio', other.cache_ratio)
  addRatio('cache_creation_ratio', other.cache_creation_ratio)
  addRatio('cache_creation_ratio_5m', other.cache_creation_ratio_5m)
  addRatio('cache_creation_ratio_1h', other.cache_creation_ratio_1h)
  addRatio('image_ratio', other.image_ratio)
  addRatio('audio_ratio', other.audio_ratio)
  addRatio('audio_completion_ratio', other.audio_completion_ratio)
  addRatio(
    other.user_group_ratio != null && other.user_group_ratio !== -1
      ? 'user_group_ratio'
      : 'group_ratio',
    groupRatio
  )
  for (const [name, value] of otherRatios) {
    addRatio(`other_ratios.${name}`, value)
  }

  const components: BillingComponent[] = []
  const addComponent = (
    key: string,
    labelKey: string,
    quantity: number,
    unitPriceUSD: number,
    unit: BillingComponentUnit,
    componentRatios: BillingRatioEntry[] = []
  ) => {
    if (!Number.isFinite(quantity) || !Number.isFinite(unitPriceUSD)) return
    const subtotalUSD =
      (Math.max(0, quantity) / componentDivisor(unit)) *
      Math.max(0, unitPriceUSD) *
      componentMultiplier
    components.push({
      key,
      labelKey,
      quantity: Math.max(0, quantity),
      unitPriceUSD: Math.max(0, unitPriceUSD),
      unit,
      ratios: componentRatios,
      subtotalUSD,
    })
  }

  const isPerCall = finiteNumber(other.model_price, -1) > 0
  const modelRatio = finiteNumber(other.model_ratio)
  const baseInputPriceUSD = modelRatio * 2

  if (!isTiered && isPerCall) {
    addComponent(
      'model',
      'Model Price',
      1,
      finiteNumber(other.model_price),
      'call'
    )
  } else if (!isTiered && other.model_ratio != null) {
    if (other.ws || other.audio) {
      addComponent(
        'text_input',
        'Text Input',
        finiteNumber(other.text_input),
        baseInputPriceUSD,
        'million_tokens',
        [{ name: 'model_ratio', value: modelRatio }]
      )
      addComponent(
        'text_output',
        'Text Output',
        finiteNumber(other.text_output),
        baseInputPriceUSD * finiteNumber(other.completion_ratio),
        'million_tokens',
        [
          { name: 'model_ratio', value: modelRatio },
          {
            name: 'completion_ratio',
            value: finiteNumber(other.completion_ratio),
          },
        ]
      )
      addComponent(
        'audio_input',
        'Audio Input',
        finiteNumber(other.audio_input),
        baseInputPriceUSD * finiteNumber(other.audio_ratio),
        'million_tokens',
        [
          { name: 'model_ratio', value: modelRatio },
          { name: 'audio_ratio', value: finiteNumber(other.audio_ratio) },
        ]
      )
      addComponent(
        'audio_output',
        'Audio Output',
        finiteNumber(other.audio_output),
        baseInputPriceUSD *
          finiteNumber(other.audio_ratio) *
          finiteNumber(other.audio_completion_ratio),
        'million_tokens',
        [
          { name: 'model_ratio', value: modelRatio },
          { name: 'audio_ratio', value: finiteNumber(other.audio_ratio) },
          {
            name: 'audio_completion_ratio',
            value: finiteNumber(other.audio_completion_ratio),
          },
        ]
      )
    } else {
      addComponent(
        'input',
        'Input',
        billableInputTokens,
        baseInputPriceUSD,
        'million_tokens',
        [{ name: 'model_ratio', value: modelRatio }]
      )
      addComponent(
        'output',
        'Output',
        log.completion_tokens,
        baseInputPriceUSD * finiteNumber(other.completion_ratio),
        'million_tokens',
        [
          { name: 'model_ratio', value: modelRatio },
          {
            name: 'completion_ratio',
            value: finiteNumber(other.completion_ratio),
          },
        ]
      )
      if (other.cache_ratio != null || cacheReadTokens > 0) {
        addComponent(
          'cache_read',
          'Cache Read',
          cacheReadTokens,
          baseInputPriceUSD * finiteNumber(other.cache_ratio),
          'million_tokens',
          [
            { name: 'model_ratio', value: modelRatio },
            { name: 'cache_ratio', value: finiteNumber(other.cache_ratio) },
          ]
        )
      }
      if (splitCacheWriteTokens > 0) {
        if (genericCacheWriteTokens > 0) {
          addComponent(
            'cache_write',
            'Cache Write',
            genericCacheWriteTokens,
            baseInputPriceUSD * finiteNumber(other.cache_creation_ratio),
            'million_tokens'
          )
        }
        addComponent(
          'cache_write_5m',
          'Cache Write (5m)',
          cacheWrite5mTokens,
          baseInputPriceUSD * finiteNumber(other.cache_creation_ratio_5m),
          'million_tokens',
          [
            { name: 'model_ratio', value: modelRatio },
            {
              name: 'cache_creation_ratio_5m',
              value: finiteNumber(other.cache_creation_ratio_5m),
            },
          ]
        )
        addComponent(
          'cache_write_1h',
          'Cache Write (1h)',
          cacheWrite1hTokens,
          baseInputPriceUSD * finiteNumber(other.cache_creation_ratio_1h),
          'million_tokens',
          [
            { name: 'model_ratio', value: modelRatio },
            {
              name: 'cache_creation_ratio_1h',
              value: finiteNumber(other.cache_creation_ratio_1h),
            },
          ]
        )
      } else if (
        other.cache_creation_pricing_configured === true ||
        cacheWriteTokens > 0
      ) {
        addComponent(
          'cache_write',
          'Cache Write',
          cacheWriteTokens,
          baseInputPriceUSD * finiteNumber(other.cache_creation_ratio),
          'million_tokens',
          [
            { name: 'model_ratio', value: modelRatio },
            {
              name: 'cache_creation_ratio',
              value: finiteNumber(other.cache_creation_ratio),
            },
          ]
        )
      }
      if (other.image || imageInputTokens > 0) {
        addComponent(
          'image_input',
          'Image input',
          imageInputTokens,
          baseInputPriceUSD * finiteNumber(other.image_ratio),
          'million_tokens',
          [
            { name: 'model_ratio', value: modelRatio },
            { name: 'image_ratio', value: finiteNumber(other.image_ratio) },
          ]
        )
      }
      if (other.audio_input_seperate_price || audioInputTokens > 0) {
        addComponent(
          'audio_input',
          'Audio input',
          audioInputTokens,
          finiteNumber(other.audio_input_price),
          'million_tokens'
        )
      }
    }
  }

  if (other.web_search && finiteNumber(other.web_search_call_count) > 0) {
    addComponent(
      'web_search',
      'Web Search',
      finiteNumber(other.web_search_call_count),
      finiteNumber(other.web_search_price),
      'thousand_calls'
    )
  }
  if (other.file_search && finiteNumber(other.file_search_call_count) > 0) {
    addComponent(
      'file_search',
      'File Search',
      finiteNumber(other.file_search_call_count),
      finiteNumber(other.file_search_price),
      'thousand_calls'
    )
  }
  if (other.image_generation_call) {
    addComponent(
      'image_generation',
      'Image Generation',
      1,
      finiteNumber(other.image_generation_call_price),
      'call'
    )
  }

  const calculatedCostUSD = components.reduce(
    (total, component) => total + component.subtotalUSD,
    0
  )

  return {
    components,
    ratios,
    effectiveGroupRatio: groupRatio,
    otherRatioMultiplier,
    effectiveMultiplier,
    calculatedCostUSD,
    rawQuota: calculatedCostUSD * quotaPerUnit,
    quotaPerUnit,
    totalInputTokens,
    billableInputTokens,
    cacheReadTokens,
    cacheWriteTokens,
    cacheWrite5mTokens,
    cacheWrite1hTokens,
  }
}
