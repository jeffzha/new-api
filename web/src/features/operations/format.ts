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
import dayjs from 'dayjs'

export function formatCompact(value: number, digits = 1) {
  return new Intl.NumberFormat(undefined, {
    notation: 'compact',
    maximumFractionDigits: digits,
  }).format(value)
}

export function formatPercent(value: number, digits = 2) {
  return `${(value * 100).toFixed(digits)}%`
}

export function formatMilliseconds(value: number) {
  if (value < 1000) return `${Math.round(value)} ms`
  return `${(value / 1000).toFixed(2)} s`
}

export function formatBytesPerSecond(value: number) {
  if (value < 1024) return `${value.toFixed(0)} B/s`
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KiB/s`
  return `${(value / 1024 / 1024).toFixed(1)} MiB/s`
}

export function formatTimestamp(value: number) {
  if (!value) return '-'
  const milliseconds = value > 1_000_000_000_000 ? value : value * 1000
  return dayjs(milliseconds).format('YYYY-MM-DD HH:mm:ss')
}
