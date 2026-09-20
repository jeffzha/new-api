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
import type { TwoFAStatus } from '@/features/profile/types'
import { api } from '@/lib/api'
import {
  AuthOperationError,
  authRequestOptions,
  authResult,
} from '@/lib/secure-verification'

export interface AccessTokenStatus {
  exists: boolean
  token_ref: string
  created_at: number | null
  last_used_at: number | null
  last_used_ip: string
}

export interface MCPAccessCredential {
  id: number
  name: string
  token_prefix: string
  scope: string
  allow_ips: string
  created_at: number
  expires_at: number | null
  last_used_at: number | null
  revoked_at: number | null
}

interface CreateMCPAccessCredentialResponse {
  credential: MCPAccessCredential
  token: string
  endpoint: string
  warning: string
}

export function getMCPAccessCredentials(): Promise<MCPAccessCredential[]> {
  return authResult(
    api.get('/api/mcp/credentials', authRequestOptions),
    'Failed to load MCP credentials'
  )
}

export function createMCPAccessCredential(
  payload: { name: string; allow_ips?: string; expires_at?: number },
  proofToken: string,
  signal: AbortSignal
): Promise<CreateMCPAccessCredentialResponse> {
  return authResult(
    api.post('/api/mcp/credentials', payload, {
      ...authRequestOptions,
      headers: { 'X-Security-Proof': proofToken },
      singleUseAuthorization: true,
      signal,
    }),
    'Failed to create MCP credential'
  )
}

export function revokeMCPAccessCredential(
  id: number,
  proofToken: string,
  signal: AbortSignal
): Promise<void> {
  return authResult(
    api.delete(`/api/mcp/credentials/${id}`, {
      ...authRequestOptions,
      headers: { 'X-Security-Proof': proofToken },
      singleUseAuthorization: true,
      signal,
    }),
    'Failed to revoke MCP credential'
  )
}

export function rotateMCPAccessCredential(
  id: number,
  proofToken: string,
  signal: AbortSignal
): Promise<CreateMCPAccessCredentialResponse> {
  return authResult(
    api.post(`/api/mcp/credentials/${id}/rotate`, undefined, {
      ...authRequestOptions,
      headers: { 'X-Security-Proof': proofToken },
      singleUseAuthorization: true,
      signal,
    }),
    'Failed to rotate MCP credential'
  )
}

export function getAccessTokenStatus(): Promise<AccessTokenStatus> {
  return authResult(
    api.get('/api/user/token/status', authRequestOptions),
    'Failed to load token status'
  )
}

export async function createAccessToken(
  proofToken: string,
  signal: AbortSignal
): Promise<string> {
  const token = await authResult<string>(
    api.post('/api/user/token', undefined, {
      ...authRequestOptions,
      headers: { 'X-Security-Proof': proofToken },
      singleUseAuthorization: true,
      signal,
    }),
    'Failed to generate token'
  )
  if (!token) throw new AuthOperationError('Failed to generate token')
  return token
}

export async function revokeAccessToken(
  proofToken: string,
  signal: AbortSignal
): Promise<void> {
  await authResult<null>(
    api.delete('/api/user/token', {
      ...authRequestOptions,
      headers: { 'X-Security-Proof': proofToken },
      singleUseAuthorization: true,
      signal,
    }),
    'Failed to revoke token'
  )
}

export interface TwoFASetupData {
  secret: string
  qr_code_data: string
  backup_codes: string[]
  flow_token: string
  expires_at: number
}

export function get2FAStatus(): Promise<TwoFAStatus> {
  return authResult(api.get('/api/user/2fa/status', authRequestOptions))
}

export function setup2FA(
  proofToken: string,
  signal: AbortSignal
): Promise<TwoFASetupData> {
  return authResult(
    api.post('/api/user/2fa/setup', undefined, {
      ...authRequestOptions,
      signal,
      headers: { 'X-Security-Proof': proofToken },
    })
  )
}

export function enable2FA(
  code: string,
  flowToken: string,
  signal: AbortSignal
): Promise<unknown> {
  return authResult(
    api.post(
      '/api/user/2fa/enable',
      { code, flow_token: flowToken },
      {
        ...authRequestOptions,
        signal,
        acceptAuthRotation: true,
        singleUseAuthorization: true,
      }
    )
  )
}
