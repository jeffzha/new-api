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
import type { QuotaDataItem } from '../types'

/** Aggregate hourly API rows without inventing values for missing time buckets. */
export function buildSignalUsage(rows: QuotaDataItem[]) {
  const buckets = new Map<number, { timestamp: number; requests: number }>()
  let requests = 0
  let quota = 0
  let tokens = 0
  for (const row of rows) {
    const count = Number(row.count) || 0
    requests += count
    quota += Number(row.quota) || 0
    tokens += Number(row.token_used) || 0
    const bucket = buckets.get(row.created_at) ?? {
      timestamp: row.created_at,
      requests: 0,
    }
    bucket.requests += count
    buckets.set(row.created_at, bucket)
  }
  return {
    requests,
    quota,
    tokens,
    series: [...buckets.values()].sort((a, b) => a.timestamp - b.timestamp),
  }
}
