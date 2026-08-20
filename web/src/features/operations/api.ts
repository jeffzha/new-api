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
import { api } from '@/lib/api'

import type {
  OpsApiResponse,
  OpsConcurrencyLimit,
  OpsFilter,
  OpsSettings,
  OpsSnapshot,
  OpsRequestDetail,
} from './types'

export async function getOpsSnapshot(filter: OpsFilter) {
  const res = await api.get<OpsApiResponse<OpsSnapshot>>(
    '/api/ops/dashboard/snapshot',
    { params: filter }
  )
  return res.data
}

export async function getOpsSettings() {
  const res = await api.get<OpsApiResponse<OpsSettings>>('/api/ops/settings')
  return res.data
}

export async function getOpsRequestDetail(requestId: string) {
  const res = await api.get<OpsApiResponse<OpsRequestDetail>>(
    `/api/ops/requests/${encodeURIComponent(requestId)}`
  )
  return res.data
}

export async function updateOpsSettings(settings: OpsSettings) {
  const res = await api.put<OpsApiResponse<OpsSettings>>(
    '/api/ops/settings',
    settings
  )
  return res.data
}

export async function listOpsConcurrencyLimits() {
  const res = await api.get<OpsApiResponse<OpsConcurrencyLimit[]>>(
    '/api/ops/concurrency-limits'
  )
  return res.data
}

export async function upsertOpsConcurrencyLimit(limit: OpsConcurrencyLimit) {
  const res = await api.put<OpsApiResponse<OpsConcurrencyLimit>>(
    '/api/ops/concurrency-limits',
    limit
  )
  return res.data
}

export async function deleteOpsConcurrencyLimit(
  channelId: number,
  keyIndex: number
) {
  const res = await api.delete<OpsApiResponse<{ deleted: boolean }>>(
    `/api/ops/concurrency-limits/${channelId}`,
    { params: { key_index: keyIndex } }
  )
  return res.data
}
