import { api } from '@/lib/http-client'
import { requireServerSuccess } from '@/lib/server-error-message'

export type Selection = { channel_id: number; model: string; endpoint: string }
export type Sample = {
  source: string
  state: string
  stage: string
  reason: string
  at: number
  duration_ms: number
  status_code: number
  request_id: string
  run_id: number
  key_index: number
  evidence_id?: number
}
export type Run = Selection & {
  id: number
  upstream: string
  source: string
  state: string
  stage: string
  reason: string
  status_code: number
  actual_micro: number | null
  created_at: number
  request_id: string
  upstream_task_id: string
}
export type Endpoint = {
  name: string
  state: string
  stale: boolean
  configured: boolean
  template_state?: 'missing' | 'draft' | 'ready'
  next_at: number
  run?: Run
  last_user: number
  last_probe: number
  last_probe_source: string
  user_count: number
  user_ok: number
  probe_count: number
  probe_ok: number
  excluded: number
  recent: Sample[]
}
export type Row = {
  id: string
  channel_id: number
  channel: string
  model: string
  upstream: string
  type: string
  groups: string[]
  state: string
  paused: boolean
  details: Endpoint[]
}
export type Overview = {
  rows: Row[]
  total: number
  counts: Record<string, number>
  outcomes?: {
    user_count: number
    user_ok: number
    probe_count: number
    probe_ok: number
  }
  groups?: string[]
  endpoints?: string[]
}
export type Access = {
  probe: boolean
  rules: boolean
  notify: boolean
  export: boolean
  worker: boolean
}
export type Plan = {
  template: Record<string, unknown> & {
    max_micro: number
    path: string
    body: Record<string, unknown>
    draft_reason?: string
  }
  revision: string
  ready: boolean
  blocked: string[]
  rule: Record<string, unknown>
  scope: string
  kind: string
}
export type Destination = {
  id: string
  name: string
  kind: string
  enabled: boolean
}
export type Delivery = {
  id: string
  target: string
  event: string
  state: string
  created_at: number
  attempts: number
  next_at: number
  status_code: number
}
export type Page<T> = { items: T[]; has_more: boolean }
export type OpsCosts = {
  state: 'confirmed' | 'unavailable' | 'unconfigured'
  amounts: Record<string, number>
  week_micro?: number
  total_micro?: number
}

const base = '/api/model-health'
type Envelope<T> = { success: boolean; message?: string; data: T }

export async function loadHealth<T>(
  path: string,
  params?: object,
  signal?: AbortSignal
): Promise<T> {
  return requireServerSuccess(
    (
      await api.get<Envelope<T>>(base + path, {
        params,
        signal,
        disableDuplicate: Boolean(signal),
      })
    ).data
  ).data
}

export async function saveHealth(
  path: string,
  data: object,
  method: 'put' | 'post' = 'put'
): Promise<void> {
  requireServerSuccess(
    (await api.request<Envelope<unknown>>({ method, url: base + path, data }))
      .data
  )
}

export async function downloadEvidence(
  id: number,
  user = false
): Promise<void> {
  const result = await api.get<Blob>(
    `${base}/${user ? 'user-evidence' : 'evidence'}/${id}`,
    { responseType: 'blob' }
  )
  const url = URL.createObjectURL(result.data)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = `model-health-${id}.md`
  anchor.click()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}

export function when(seconds: number): string {
  return seconds > 0 ? new Date(seconds * 1000).toLocaleString() : '\u2014'
}

export function money(micro: number): string {
  return `\u00a5${(micro / 1_000_000).toFixed(6)}`
}
