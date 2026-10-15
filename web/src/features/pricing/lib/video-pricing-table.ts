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
/**
 * Video catalogue price list.
 *
 * Video models price by output duration and resolution, and some providers
 * pass material (reference image / video / audio) cost through from the
 * upstream. The generic tier breakdown cannot express that shape and falls
 * back to printing the raw expression, so this module rebuilds the published
 * list from data the catalogue already ships: the billing expression, the
 * declared usage schema and the labelled usage examples.
 *
 * Every price comes from that configured data. Change the billing expression
 * or the plugin's usage examples and the list follows; nothing is hard coded.
 */
import type {
  BillingUsageFieldSchema,
  BillingUsageSchema,
  MaterialPricingCatalog,
  PricingModel,
} from '../types'
import { compileBillingExpression } from './billing-expression/parser'
import { visitExpression } from './billing-expression/types'
import { evaluateTaskVisualConfig, tryParseTaskVisualConfig } from './task-expr'
import { getTaskPricingDisplayTiers } from './task-matrix-display'

const RESOLUTION_FIELD = 'resolution'
const SECONDS_FIELD = 'seconds'
// Output/cost bookkeeping fields describe revenue or vendor spend, never a
// material surcharge, so the material table ignores them.
const OUTPUT_ONLY_FIELD = /^(output|vendor)_/i
const MATERIAL_CREDIT_FIELD = /material/i

export type VideoOutputPriceRow = {
  key: string
  resolution: string
  /** `second` bills by output duration; `request` bills a fixed-length job. */
  rule: 'second' | 'request'
  /** USD per second for `second` rows, USD per whole request for `request` rows. */
  amountUSD: number
  /** Duration the vendor fixes for `request` rows, when it is known. */
  seconds?: number
}

export type VideoMaterialFieldRow = {
  field: string
  definition: BillingUsageFieldSchema
  /** USD rates by output tier; a tier-independent price has no label. */
  rates: Array<{ label?: string; amountUSD: number }>
}

export type VideoMaterialPricing =
  | { kind: 'catalog'; catalog: MaterialPricingCatalog }
  /** Materials settle from the upstream invoice; credits carry the USD rate. */
  | { kind: 'upstream_credits'; creditPriceUSD: number }
  /** Materials bill per declared unit from the configured expression. */
  | { kind: 'usage_fields'; rows: VideoMaterialFieldRow[] }

export type VideoPricingTable = {
  modelLabel: string
  outputRows: VideoOutputPriceRow[]
  /** Absent when the expression prices no material, so materials add nothing. */
  materialPricing?: VideoMaterialPricing
}

function isPricedNumberField(
  definition: BillingUsageFieldSchema | undefined
): boolean {
  return definition?.type === 'number' && Boolean(definition.unit)
}

/**
 * Catalogue values the expression itself switches on. A plugin schema can lag
 * behind the configured expression (a tier exists in the expression but not in
 * the declared enum), and that tier still has to expand into a price row.
 */
function expressionEnumValues(
  expression: string | null | undefined
): Map<string, string[]> {
  const values = new Map<string, string[]>()
  const compiled = compileBillingExpression(expression || '')
  if (compiled.status !== 'ready') return values
  visitExpression(compiled.ast, (node) => {
    if (node.kind !== 'binary' || node.operator !== '==') return
    if (node.left.kind !== 'call' || node.left.name !== 'u') return
    const key = node.left.args[0]
    if (
      !key ||
      key.kind !== 'literal' ||
      typeof key.value !== 'string' ||
      node.right.kind !== 'literal' ||
      typeof node.right.value !== 'string'
    ) {
      return
    }
    const current = values.get(key.value) ?? []
    if (!current.includes(node.right.value)) current.push(node.right.value)
    values.set(key.value, current)
  })
  return values
}

function mergeFieldDefinition(
  declared: BillingUsageFieldSchema | undefined,
  added: BillingUsageFieldSchema
): BillingUsageFieldSchema {
  if (!declared) return { ...added }
  const values = [...new Set([...(declared.enum ?? []), ...(added.enum ?? [])])]
  const merged: BillingUsageFieldSchema = { ...declared }
  if (!merged.type && added.type) merged.type = added.type
  if (!merged.unit && added.unit) merged.unit = added.unit
  if (values.length > 0) merged.enum = values
  return merged
}

/**
 * Use only the selected provider's schema and expression. Other providers can
 * expose the same model with different resolutions and material charges.
 */
function declaredSchema(model: PricingModel): BillingUsageSchema {
  const merged: BillingUsageSchema = {}
  const sources: (BillingUsageSchema | undefined)[] = [
    model.billing_usage_schema,
  ]
  const expressions: (string | null | undefined)[] = [model.billing_expr]
  for (const source of sources) {
    for (const [field, definition] of Object.entries(source ?? {})) {
      merged[field] = mergeFieldDefinition(merged[field], definition)
    }
  }
  for (const expression of expressions) {
    for (const [field, values] of expressionEnumValues(expression)) {
      const declared = merged[field]
      if (!declared) continue
      merged[field] = {
        ...declared,
        enum: [...new Set([...(declared.enum ?? []), ...values])],
      }
    }
  }
  return merged
}

function pickFields(
  schema: BillingUsageSchema
): { resolution: string; seconds: string } | null {
  const entries = Object.entries(schema)
  const seconds =
    entries.find(
      ([field, definition]) =>
        field === SECONDS_FIELD && isPricedNumberField(definition)
    ) ??
    entries.find(
      ([, definition]) =>
        isPricedNumberField(definition) && definition.unit === 'second'
    )
  const resolution =
    entries.find(
      ([field, definition]) =>
        field === RESOLUTION_FIELD && Boolean(definition.enum?.length)
    ) ?? entries.find(([, definition]) => Boolean(definition.enum?.length))
  if (!seconds || !resolution) return null
  return { seconds: seconds[0], resolution: resolution[0] }
}

function tierResolution(
  conditions: { field: string; value: string }[],
  resolutionField: string,
  fallback: string
): string {
  return (
    conditions.find((condition) => condition.field === resolutionField)
      ?.value ?? fallback
  )
}

function outputRowsOfTiers(
  model: PricingModel,
  fields: { resolution: string; seconds: string },
  schema: BillingUsageSchema
): VideoOutputPriceRow[] {
  const seen = new Set<string>()
  const rows: VideoOutputPriceRow[] = []
  for (const tier of getTaskPricingDisplayTiers(model.billing_expr, schema)) {
    if (!('unitPrices' in tier)) continue
    const resolution = tierResolution(
      tier.conditions,
      fields.resolution,
      tier.label
    ).trim()
    if (!resolution || seen.has(resolution)) continue
    const perSecond = Number(tier.unitPrices[fields.seconds])
    if (Number.isFinite(perSecond) && perSecond > 0) {
      seen.add(resolution)
      rows.push({
        key: `${resolution}-second`,
        resolution,
        rule: 'second',
        amountUSD: perSecond,
      })
      continue
    }
    if (Number.isFinite(tier.constant) && tier.constant > 0) {
      seen.add(resolution)
      rows.push({
        key: `${resolution}-request`,
        resolution,
        rule: 'request',
        amountUSD: tier.constant,
      })
    }
  }
  return rows
}

/**
 * The labelled examples the task plugin ships describe whole catalogue
 * requests. A catalogue that fixes the duration advertises the request total;
 * otherwise each example yields the per-second rate the expression charges.
 */
function outputRowsOfExamples(
  model: PricingModel,
  fields: { resolution: string; seconds: string },
  schema: BillingUsageSchema
): VideoOutputPriceRow[] {
  const examples = model.billing_usage_examples ?? []
  const config = tryParseTaskVisualConfig(model.billing_expr, schema)
  if (!config || examples.length === 0) return []
  const durations = examples
    .map((example) => Number(example.facts[fields.seconds]))
    .filter((value) => Number.isFinite(value) && value > 0)
  const fixedSeconds =
    durations.length > 0 && durations.every((value) => value === durations[0])
      ? durations[0]
      : undefined

  const seen = new Set<string>()
  const rows: VideoOutputPriceRow[] = []
  for (const example of examples) {
    const evaluation = evaluateTaskVisualConfig(config, example.facts, schema)
    if (!evaluation || evaluation.total <= 0) continue
    const resolution = String(example.facts[fields.resolution] ?? '').trim()
    const seconds = Number(example.facts[fields.seconds])
    if (!resolution || !Number.isFinite(seconds) || seconds <= 0) continue
    const key = `${resolution}-${fixedSeconds ? 'request' : 'second'}`
    if (seen.has(key)) continue
    seen.add(key)
    if (fixedSeconds) {
      rows.push({
        key,
        resolution,
        rule: 'request',
        amountUSD: evaluation.total,
        seconds,
      })
      continue
    }
    rows.push({
      key,
      resolution,
      rule: 'second',
      amountUSD: evaluation.total / seconds,
    })
  }
  return rows
}

/** 768P ranks above 480P and 2K above 1080P; unranked labels keep schema order. */
function resolutionRank(resolution: string): number | null {
  const match = resolution.trim().match(/^(\d+(?:\.\d+)?)\s*([pPkK])$/)
  if (!match) return null
  const value = Number(match[1])
  return match[2]?.toUpperCase() === 'K' ? value * 1000 : value
}

function orderRows(
  rows: VideoOutputPriceRow[],
  definition: BillingUsageFieldSchema | undefined
): VideoOutputPriceRow[] {
  const declared = definition?.enum ?? []
  return [...rows].sort((left, right) => {
    const leftRank = resolutionRank(left.resolution)
    const rightRank = resolutionRank(right.resolution)
    if (leftRank !== null && rightRank !== null && leftRank !== rightRank) {
      return leftRank - rightRank
    }
    if (leftRank !== null && rightRank === null) return -1
    if (leftRank === null && rightRank !== null) return 1
    const leftIndex = declared.indexOf(left.resolution)
    const rightIndex = declared.indexOf(right.resolution)
    if (leftIndex !== rightIndex) {
      if (leftIndex === -1) return 1
      if (rightIndex === -1) return -1
      return leftIndex - rightIndex
    }
    return left.resolution.localeCompare(right.resolution)
  })
}

function pickOutputRows(
  fromTiers: VideoOutputPriceRow[],
  fromExamples: VideoOutputPriceRow[]
): VideoOutputPriceRow[] {
  // A fixed-duration catalogue advertises whole-request totals, which only the
  // examples carry. Otherwise the expression itself is the published rate.
  if (fromExamples.some((row) => row.rule === 'request')) return fromExamples
  return fromTiers.length > 0 ? fromTiers : fromExamples
}

function materialFieldRows(
  model: PricingModel,
  fields: { resolution: string; seconds: string },
  schema: BillingUsageSchema
): VideoMaterialFieldRow[] {
  const variants = [model]
  const rows: VideoMaterialFieldRow[] = []
  for (const candidate of variants) {
    for (const tier of getTaskPricingDisplayTiers(
      candidate.billing_expr,
      schema
    )) {
      if (!('unitPrices' in tier)) continue
      for (const [field, rawPrice] of Object.entries(tier.unitPrices)) {
        if (field === fields.seconds || field === fields.resolution) continue
        if (OUTPUT_ONLY_FIELD.test(field)) continue
        const definition = schema[field]
        if (!isPricedNumberField(definition)) continue
        const amountUSD = Number(rawPrice)
        if (!Number.isFinite(amountUSD) || amountUSD <= 0) continue
        const tierLabel = tier.conditions.find(
          (condition) => condition.field === fields.resolution
        )?.value
        const existing = rows.find((row) => row.field === field)
        if (!existing) {
          rows.push({
            field,
            definition,
            rates: [{ ...(tierLabel ? { label: tierLabel } : {}), amountUSD }],
          })
          continue
        }
        if (
          existing.rates.some(
            (rate) => rate.label === tierLabel && rate.amountUSD === amountUSD
          )
        ) {
          continue
        }
        existing.rates.push({
          ...(tierLabel ? { label: tierLabel } : {}),
          amountUSD,
        })
      }
    }
  }
  return rows.map((row) => {
    const first = row.rates[0]
    if (
      first &&
      row.rates.every((rate) => rate.amountUSD === first.amountUSD)
    ) {
      return { ...row, rates: [{ amountUSD: first.amountUSD }] }
    }
    return row
  })
}

function materialPricingOf(
  model: PricingModel,
  fields: { resolution: string; seconds: string },
  schema: BillingUsageSchema
): VideoMaterialPricing | null {
  if (model.billing_material_pricing?.items.length) {
    return { kind: 'catalog', catalog: model.billing_material_pricing }
  }
  const rows = materialFieldRows(model, fields, schema)
  const passThrough = rows.find(
    (row) =>
      row.definition.unit === 'credit' && MATERIAL_CREDIT_FIELD.test(row.field)
  )
  const passThroughRate = passThrough?.rates[0]
  if (passThroughRate) {
    return {
      kind: 'upstream_credits',
      creditPriceUSD: passThroughRate.amountUSD,
    }
  }
  return rows.length > 0 ? { kind: 'usage_fields', rows } : null
}

/**
 * Returns the published video price list, or null when the model does not
 * describe a resolution/duration catalogue or prices no output row. Callers
 * keep their existing display when this returns null.
 */
export function getVideoPricingTable(
  model: PricingModel
): VideoPricingTable | null {
  if (model.billing_mode !== 'tiered_expr' || !model.billing_expr?.trim()) {
    return null
  }
  const schema = declaredSchema(model)
  const fields = pickFields(schema)
  if (!fields) return null
  const outputRows = pickOutputRows(
    outputRowsOfTiers(model, fields, schema),
    outputRowsOfExamples(model, fields, schema)
  )
  if (outputRows.length === 0) return null
  const materialPricing = materialPricingOf(model, fields, schema)
  return {
    modelLabel: model.model_name,
    outputRows: orderRows(outputRows, schema[fields.resolution]),
    ...(materialPricing ? { materialPricing } : {}),
  }
}
