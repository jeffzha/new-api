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

export interface AgentStoreItem {
  id: string
  slug: string
  display_name: string
  summary: string
  description?: string
  avatar_url?: string
  category: string
  tags: string[]
  featured: boolean
  app_mode: number
  runtime_profile: string
  capabilities: string[]
  available: boolean
}

export interface AgentStoreList {
  items: AgentStoreItem[]
  next_cursor?: string
}

export interface AgentLaunchResult {
  redirect_url: string
  expires_at: string
}

export interface AgentStoreEnvelope<T> {
  success: boolean
  data?: T
  error?: {
    code?: string
    message?: string
    request_id?: string
  }
}

export class AgentStoreApiError extends Error {
  readonly status: number
  readonly code?: string

  constructor(message: string, status: number, code?: string) {
    super(message)
    this.name = 'AgentStoreApiError'
    this.status = status
    this.code = code
  }
}
