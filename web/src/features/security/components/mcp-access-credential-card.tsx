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
import { KeyRound, Plus, RefreshCw, ShieldCheck, Trash2 } from 'lucide-react'
import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { CopyButton } from '@/components/copy-button'
import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  SecureVerificationDialog,
  useSecureVerification,
} from '@/features/auth/secure-verification'
import dayjs from '@/lib/dayjs'
import { handleServerError } from '@/lib/handle-server-error'

import {
  createMCPAccessCredential,
  getMCPAccessCredentials,
  revokeMCPAccessCredential,
  rotateMCPAccessCredential,
  type MCPAccessCredential,
} from '../api'

export function MCPAccessCredentialCard() {
  const { t } = useTranslation()
  const { cancel, dialogProps, requestVerification } = useSecureVerification()
  const [credentials, setCredentials] = useState<MCPAccessCredential[]>([])
  const [loading, setLoading] = useState(true)
  const [creating, setCreating] = useState(false)
  const [dialogOpen, setDialogOpen] = useState(false)
  const [name, setName] = useState('')
  const [allowIPs, setAllowIPs] = useState('')
  const [expiresAt, setExpiresAt] = useState('')
  const [generated, setGenerated] = useState<{
    token: string
    endpoint: string
  } | null>(null)
  const activeRequest = useRef<AbortController | null>(null)

  const loadCredentials = useCallback(async () => {
    setLoading(true)
    try {
      setCredentials(await getMCPAccessCredentials())
    } catch (error) {
      handleServerError(error, t('Failed to load MCP credentials'))
    } finally {
      setLoading(false)
    }
  }, [t])

  useEffect(() => {
    void loadCredentials()
  }, [loadCredentials])

  useEffect(
    () => () => {
      activeRequest.current?.abort()
      cancel()
    },
    [cancel]
  )

  const createCredential = useCallback(async () => {
    if (creating || !name.trim()) return
    const expiresAtSeconds = expiresAt
      ? Math.floor(new Date(expiresAt).getTime() / 1000)
      : undefined
    const proof = await requestVerification({
      scope: 'mcp.access.manage',
    })
    if (!proof) return
    const controller = new AbortController()
    activeRequest.current = controller
    setCreating(true)
    try {
      const result = await createMCPAccessCredential(
        {
          name: name.trim(),
          allow_ips: allowIPs.trim() || undefined,
          expires_at: expiresAtSeconds,
        },
        proof.proof_token,
        controller.signal
      )
      setGenerated({ token: result.token, endpoint: result.endpoint })
      setName('')
      setAllowIPs('')
      setExpiresAt('')
      setDialogOpen(false)
      await loadCredentials()
    } catch (error) {
      if (!controller.signal.aborted) handleServerError(error)
    } finally {
      if (activeRequest.current === controller) {
        activeRequest.current = null
        setCreating(false)
      }
    }
  }, [
    allowIPs,
    creating,
    expiresAt,
    loadCredentials,
    name,
    requestVerification,
  ])

  const revokeCredential = useCallback(
    async (credential: MCPAccessCredential) => {
      if (creating || credential.revoked_at) return
      const proof = await requestVerification({
        scope: 'mcp.access.manage',
      })
      if (!proof) return
      const controller = new AbortController()
      activeRequest.current = controller
      setCreating(true)
      try {
        await revokeMCPAccessCredential(
          credential.id,
          proof.proof_token,
          controller.signal
        )
        toast.success(t('MCP credential revoked'))
        await loadCredentials()
      } catch (error) {
        if (!controller.signal.aborted) handleServerError(error)
      } finally {
        if (activeRequest.current === controller) {
          activeRequest.current = null
          setCreating(false)
        }
      }
    },
    [creating, loadCredentials, requestVerification, t]
  )

  const rotateCredential = useCallback(
    async (credential: MCPAccessCredential) => {
      if (creating || credential.revoked_at) return
      const proof = await requestVerification({
        scope: 'mcp.access.manage',
      })
      if (!proof) return
      const controller = new AbortController()
      activeRequest.current = controller
      setCreating(true)
      try {
        const result = await rotateMCPAccessCredential(
          credential.id,
          proof.proof_token,
          controller.signal
        )
        setGenerated({ token: result.token, endpoint: result.endpoint })
        toast.success(t('MCP credential rotated'))
        await loadCredentials()
      } catch (error) {
        if (!controller.signal.aborted) handleServerError(error)
      } finally {
        if (activeRequest.current === controller) {
          activeRequest.current = null
          setCreating(false)
        }
      }
    },
    [creating, loadCredentials, requestVerification, t]
  )

  const mcpClientConfig = generated
    ? JSON.stringify(
        {
          mcpServers: {
            'NEXIGHT pricing': {
              url: generated.endpoint,
              headers: { Authorization: `Bearer ${generated.token}` },
            },
          },
        },
        null,
        2
      )
    : ''

  let credentialsContent: ReactNode
  if (loading) {
    credentialsContent = (
      <p role='status' className='text-muted-foreground text-xs'>
        {t('Loading...')}
      </p>
    )
  } else if (credentials.length === 0) {
    credentialsContent = (
      <p className='text-muted-foreground rounded-md border border-dashed px-3 py-4 text-center text-xs'>
        {t('No MCP credentials yet')}
      </p>
    )
  } else {
    credentialsContent = (
      <div className='divide-y rounded-md border'>
        {credentials.map((credential) => (
          <div
            key={credential.id}
            className='flex flex-wrap items-center justify-between gap-3 px-3 py-2.5'
          >
            <div className='min-w-0 text-xs'>
              <p className='truncate font-medium'>{credential.name}</p>
              <p className='text-muted-foreground mt-1 font-mono'>
                {credential.token_prefix}…
              </p>
              <p className='text-muted-foreground mt-1'>
                {credential.expires_at
                  ? t('Expires {{time}}', {
                      time: dayjs
                        .unix(credential.expires_at)
                        .format('YYYY-MM-DD HH:mm'),
                    })
                  : t('Never expires')}
              </p>
              {credential.allow_ips && (
                <p className='text-muted-foreground mt-1 break-all'>
                  {t('Allowed IPs: {{ips}}', {
                    ips: credential.allow_ips.replaceAll('\n', ', '),
                  })}
                </p>
              )}
            </div>
            {credential.revoked_at ? (
              <span className='text-muted-foreground text-xs'>
                {t('Revoked')}
              </span>
            ) : (
              <div className='flex items-center gap-1'>
                <Button
                  size='icon-sm'
                  variant='ghost'
                  disabled={creating}
                  aria-label={t('Rotate MCP credential')}
                  title={t('Rotate MCP credential')}
                  onClick={() => void rotateCredential(credential)}
                >
                  <RefreshCw className='size-4' aria-hidden='true' />
                </Button>
                <Button
                  size='icon-sm'
                  variant='ghost'
                  disabled={creating}
                  aria-label={t('Revoke')}
                  title={t('Revoke')}
                  onClick={() => void revokeCredential(credential)}
                >
                  <Trash2
                    className='text-destructive size-4'
                    aria-hidden='true'
                  />
                </Button>
              </div>
            )}
          </div>
        ))}
      </div>
    )
  }

  return (
    <>
      <Card data-card-hover='false' className='gap-3 p-3 sm:p-4'>
        <div className='flex flex-wrap items-start justify-between gap-3'>
          <div className='min-w-0 space-y-1'>
            <div className='flex items-center gap-2'>
              <KeyRound className='text-primary size-4' aria-hidden='true' />
              <h4 className='text-sm font-semibold'>
                {t('MCP Access Credentials')}
              </h4>
            </div>
            <p className='text-muted-foreground text-xs leading-5'>
              {t(
                'This credential can only read public model pricing and default sales coefficients. It cannot call models or read platform costs.'
              )}
            </p>
          </div>
          <Button size='sm' onClick={() => setDialogOpen(true)}>
            <Plus className='size-4' aria-hidden='true' />
            {t('Create MCP Credential')}
          </Button>
        </div>
        {credentialsContent}
      </Card>
      <Dialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        title={t('Create MCP Credential')}
        description={t(
          'Create a dedicated credential for an external MCP client.'
        )}
        contentClassName='sm:max-w-md'
        footer={
          <>
            <Button
              variant='outline'
              disabled={creating}
              onClick={() => setDialogOpen(false)}
            >
              {t('Cancel')}
            </Button>
            <Button
              disabled={creating || !name.trim()}
              onClick={() => void createCredential()}
            >
              <ShieldCheck className='size-4' aria-hidden='true' />
              {t('Create MCP Credential')}
            </Button>
          </>
        }
      >
        <div className='space-y-4 py-1'>
          <div className='space-y-2'>
            <Label htmlFor='mcp-credential-name' required>
              {t('MCP credential name')}
            </Label>
            <Input
              id='mcp-credential-name'
              value={name}
              maxLength={80}
              autoComplete='off'
              onChange={(event) => setName(event.target.value)}
            />
          </div>
          <div className='space-y-2'>
            <Label htmlFor='mcp-credential-ips'>
              {t('IP allowlist (optional)')}
            </Label>
            <Input
              id='mcp-credential-ips'
              value={allowIPs}
              placeholder='203.0.113.0/24, 2001:db8::1'
              autoComplete='off'
              onChange={(event) => setAllowIPs(event.target.value)}
            />
          </div>
          <div className='space-y-2'>
            <Label htmlFor='mcp-credential-expires-at'>
              {t('Expires at (optional)')}
            </Label>
            <Input
              id='mcp-credential-expires-at'
              type='datetime-local'
              value={expiresAt}
              onChange={(event) => setExpiresAt(event.target.value)}
            />
            <p className='text-muted-foreground text-xs'>
              {t(
                'Leave blank to keep this credential valid until it is revoked.'
              )}
            </p>
          </div>
        </div>
      </Dialog>
      {generated && (
        <Dialog
          open
          onOpenChange={(open) => {
            if (!open) setGenerated(null)
          }}
          title={t('One-time MCP credential')}
          description={t(
            'Save this credential now. You will not be able to view it again after closing this dialog.'
          )}
          contentClassName='sm:max-w-xl'
          footer={
            <Button onClick={() => setGenerated(null)}>{t('Close')}</Button>
          }
        >
          <div className='space-y-4 py-1'>
            <div className='space-y-2'>
              <Label htmlFor='mcp-endpoint'>{t('MCP endpoint')}</Label>
              <div className='flex gap-2'>
                <Input id='mcp-endpoint' value={generated.endpoint} readOnly />
                <CopyButton
                  value={generated.endpoint}
                  variant='outline'
                  tooltip={t('Copy')}
                  aria-label={t('Copy')}
                />
              </div>
            </div>
            <div className='space-y-2'>
              <Label htmlFor='generated-mcp-credential'>
                {t('MCP credential')}
              </Label>
              <div className='flex gap-2'>
                <Input
                  id='generated-mcp-credential'
                  value={generated.token}
                  readOnly
                  autoComplete='off'
                  className='font-mono text-xs'
                />
                <CopyButton
                  value={generated.token}
                  variant='outline'
                  tooltip={t('Copy token')}
                  aria-label={t('Copy token')}
                />
              </div>
            </div>
            <div className='space-y-2'>
              <div className='flex items-center justify-between gap-2'>
                <Label htmlFor='mcp-client-configuration'>
                  {t('MCP client configuration')}
                </Label>
                <CopyButton
                  value={mcpClientConfig}
                  variant='outline'
                  tooltip={t('Copy configuration')}
                  aria-label={t('Copy configuration')}
                />
              </div>
              <pre
                id='mcp-client-configuration'
                className='bg-muted max-h-48 overflow-auto rounded-md border p-3 text-xs leading-5'
              >
                {mcpClientConfig}
              </pre>
              <p className='text-muted-foreground text-xs'>
                {t(
                  'Paste this JSON into an MCP client that supports a remote Streamable HTTP server.'
                )}
              </p>
            </div>
          </div>
        </Dialog>
      )}
      <SecureVerificationDialog {...dialogProps} />
    </>
  )
}
