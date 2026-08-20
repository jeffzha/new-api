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

interface SessionTicketResponse {
  success: boolean
  data?: {
    ticket?: string
  }
}

let pendingSessionTicket: Promise<string> | undefined

export function requestAgentStoreSessionTicket(): Promise<string> {
  if (pendingSessionTicket != null) return pendingSessionTicket
  pendingSessionTicket = api
    .post<SessionTicketResponse>('/api/workbench/session-ticket', undefined, {
      skipBusinessError: true,
      skipErrorHandler: true,
    })
    .then((response) => {
      const ticket = response.data.data?.ticket?.trim()
      if (!response.data.success || ticket == null || ticket === '') {
        throw new Error('Invalid workbench session ticket response')
      }
      return ticket
    })
    .finally(() => {
      pendingSessionTicket = undefined
    })
  return pendingSessionTicket
}
