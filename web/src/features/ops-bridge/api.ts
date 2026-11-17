import { api } from '@/lib/api'

export type ImportPlan = {
  id: string
  catalog_version: string
  before: Record<string, { configured: Record<string, unknown> }>
  after: Record<string, Record<string, unknown>>
  protected_models: string[]
  unsupported_models: Record<string, string>
}

export type BridgeStatus = {
  enabled: boolean
  persistence_error?: string
  enforce_calls?: boolean
  deliveries?: {
    id: string
    path: string
    status: string
    attempts: number
    last_error?: string
  }[]
  request_states?: Record<string, number>
  requests?: { id: string; status: string }[]
}

export async function getBridgeStatus(): Promise<BridgeStatus> {
  return (await api.get<BridgeStatus>('/api/ops-bridge')).data
}

export async function previewImport(): Promise<ImportPlan> {
  return (await api.post<ImportPlan>('/api/ops-bridge/imports/preview')).data
}

export async function applyImport(
  plan: ImportPlan,
  models: string[],
  overwrite: string[]
): Promise<void> {
  await api.post(
    `/api/ops-bridge/imports/${encodeURIComponent(plan.id)}/apply`,
    {
      models,
      overwrite_models: overwrite,
    }
  )
}

export async function bridgeAction(path: string): Promise<void> {
  await api.post(`/api/ops-bridge/${path}`)
}
