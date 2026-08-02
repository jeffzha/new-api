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

export type MetricTone = 'neutral' | 'info' | 'success' | 'warning' | 'critical'

export const METRIC_TONE_TEXT_CLASS: Record<MetricTone, string> = {
  neutral: 'text-foreground',
  info: 'text-info',
  success: 'text-success',
  warning: 'text-warning',
  critical: 'text-destructive',
}

export const METRIC_TONE_DOT_CLASS: Record<MetricTone, string> = {
  neutral: 'bg-neutral',
  info: 'bg-info',
  success: 'bg-success',
  warning: 'bg-warning',
  critical: 'bg-destructive',
}

export const METRIC_TONE_PROGRESS_CLASS: Record<MetricTone, string> = {
  neutral: '[&_[data-slot=progress-indicator]]:bg-neutral',
  info: '[&_[data-slot=progress-indicator]]:bg-info',
  success: '[&_[data-slot=progress-indicator]]:bg-success',
  warning: '[&_[data-slot=progress-indicator]]:bg-warning',
  critical: '[&_[data-slot=progress-indicator]]:bg-destructive',
}

function isMetricNumber(value: number | null | undefined): value is number {
  return typeof value === 'number' && Number.isFinite(value)
}

export function higherIsBetterTone(
  value: number | null | undefined,
  criticalThreshold: number | null | undefined,
  warningBuffer: number
): MetricTone {
  if (!isMetricNumber(value) || !isMetricNumber(criticalThreshold)) {
    return 'neutral'
  }
  if (value < criticalThreshold) return 'critical'
  if (value < criticalThreshold + warningBuffer) return 'warning'
  return 'success'
}

export function lowerIsBetterTone(
  value: number | null | undefined,
  criticalThreshold: number | null | undefined,
  warningRatio = 0.8
): MetricTone {
  if (!isMetricNumber(value) || !isMetricNumber(criticalThreshold)) {
    return 'neutral'
  }
  if (value >= criticalThreshold) return 'critical'
  if (value >= criticalThreshold * warningRatio) return 'warning'
  return 'success'
}

export function boundedUsageTone(
  value: number | null | undefined,
  warningThreshold: number,
  criticalThreshold: number
): MetricTone {
  if (!isMetricNumber(value)) return 'neutral'
  if (value >= criticalThreshold) return 'critical'
  if (value >= warningThreshold) return 'warning'
  return 'success'
}

export function healthScoreTone(
  score: number | null | undefined,
  state?: string
): MetricTone {
  if (state === 'idle' || !isMetricNumber(score)) return 'neutral'
  if (score >= 90) return 'success'
  if (score >= 60) return 'warning'
  return 'critical'
}

export function concurrencyLoadTone(
  loadPercent: number | null | undefined
): MetricTone {
  return boundedUsageTone(loadPercent, 50, 90)
}

export function worstMetricTone(...tones: MetricTone[]): MetricTone {
  const rank: Record<MetricTone, number> = {
    neutral: 0,
    info: 1,
    success: 2,
    warning: 3,
    critical: 4,
  }
  return tones.reduce((worst, tone) =>
    rank[tone] > rank[worst] ? tone : worst
  )
}
