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
import {
  AgentStoreApiError,
  type AgentLaunchResult,
  type AgentStoreEnvelope,
  type AgentStoreItem,
  type AgentStoreList,
} from './types'

const API_ROOT = '/api/workbench/agent-store'

async function requestAgentStore<T>(
  path: string,
  init: RequestInit = {}
): Promise<T> {
  const response = await fetch(`${API_ROOT}${path}`, {
    ...init,
    credentials: 'include',
    cache: 'no-store',
    headers: {
      Accept: 'application/json',
      ...init.headers,
    },
  })

  let envelope: AgentStoreEnvelope<T>
  try {
    envelope = (await response.json()) as AgentStoreEnvelope<T>
  } catch {
    throw new AgentStoreApiError(
      'The Agent Store returned an invalid response.',
      response.status,
      'invalid_response'
    )
  }

  if (!response.ok || !envelope.success || envelope.data == null) {
    throw new AgentStoreApiError(
      envelope.error?.message ??
        `Agent Store request failed (${response.status}).`,
      response.status,
      envelope.error?.code
    )
  }
  return envelope.data
}

export const agentStoreApi = {
  status: (signal?: AbortSignal) =>
    requestAgentStore<{ enabled: boolean }>('/status', { signal }),
  list: (
    params: { cursor?: string; category?: string; query?: string } = {},
    signal?: AbortSignal
  ) => {
    const search = new URLSearchParams()
    if (params.cursor != null && params.cursor !== '') {
      search.set('cursor', params.cursor)
    }
    if (params.category != null && params.category !== 'all') {
      search.set('category', params.category)
    }
    if (params.query != null && params.query !== '') {
      search.set('query', params.query)
    }
    const suffix = search.size === 0 ? '' : `?${search.toString()}`
    return requestAgentStore<AgentStoreList>(suffix, { signal })
  },
  detail: (slug: string, signal?: AbortSignal) =>
    requestAgentStore<AgentStoreItem>(`/${encodeURIComponent(slug)}`, {
      signal,
    }),
  launch: (slug: string) =>
    requestAgentStore<AgentLaunchResult>(
      `/${encodeURIComponent(slug)}/launch`,
      {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'X-CSRF-Token': requireControlCSRFToken(),
        },
        body: '{}',
      }
    ),
}

function requireControlCSRFToken(): string {
  const token = agentStoreCSRFToken(document.cookie)
  if (token != null) return token
  throw new AgentStoreApiError(
    'The Agent Store session is missing its CSRF token.',
    403,
    'csrf_missing'
  )
}

export function agentStoreCSRFToken(cookieHeader: string): string | undefined {
  for (const part of cookieHeader.split(';')) {
    const separator = part.indexOf('=')
    if (separator < 0) continue
    if (part.slice(0, separator).trim() !== 'claw_control_csrf') continue
    try {
      const token = decodeURIComponent(part.slice(separator + 1).trim())
      if (token !== '') return token
    } catch {
      break
    }
  }
  return undefined
}
