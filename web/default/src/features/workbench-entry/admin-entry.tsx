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
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'
import { Main } from '@/components/layout'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Spinner } from '@/components/ui/spinner'
import { SecureVerificationDialog } from '@/features/auth/secure-verification'
import { useSecureVerification } from '@/features/auth/secure-verification/hooks/use-secure-verification'
import { api } from '@/lib/api'

interface AdminSessionTicketResponse {
  success: boolean
  data?: {
    ticket?: string
  }
}

let pendingAdminSessionTicket: Promise<string> | undefined

function requestAdminSessionTicket(): Promise<string> {
  if (pendingAdminSessionTicket != null) return pendingAdminSessionTicket
  pendingAdminSessionTicket = api
    .post<AdminSessionTicketResponse>(
      '/api/admin/workbench/session-ticket',
      undefined,
      { skipBusinessError: true, skipErrorHandler: true }
    )
    .then((response) => {
      const ticket = response.data.data?.ticket?.trim()
      if (!response.data.success || ticket == null || ticket === '') {
        throw new Error('Invalid workbench admin session ticket response')
      }
      return ticket
    })
    .finally(() => {
      pendingAdminSessionTicket = undefined
    })
  return pendingAdminSessionTicket
}

async function requestAdminStepUpTicket(
  method: 'password' | 'secure_verification',
  password?: string
): Promise<string> {
  const response = await api.post<AdminSessionTicketResponse>(
    '/api/admin/workbench/step-up-ticket',
    { method, password },
    { skipBusinessError: true, skipErrorHandler: true }
  )
  const ticket = response.data.data?.ticket?.trim()
  if (!response.data.success || ticket == null || ticket === '') {
    throw new Error('Invalid workbench administrator step-up response')
  }
  return ticket
}

function enterWorkbenchAdmin(ticket: string) {
  const target = new URL('/api/workbench/entry', window.location.origin)
  target.searchParams.set('ticket', ticket)
  window.location.replace(target.toString())
}

export function AdminWorkbenchEntry() {
  const { t } = useTranslation()
  const [failed, setFailed] = useState(false)
  const [attempt, setAttempt] = useState(0)
  const [password, setPassword] = useState('')
  const [passwordPending, setPasswordPending] = useState(false)
  const stepUpMode =
    new URLSearchParams(window.location.search).get('step_up') === '1'
  const verification = useSecureVerification({
    onSuccess: (result) => enterWorkbenchAdmin(result as string),
  })

  useEffect(() => {
    if (stepUpMode) return
    let active = true
    setFailed(false)
    void requestAdminSessionTicket()
      .then((ticket) => {
        if (!active) return
        enterWorkbenchAdmin(ticket)
      })
      .catch(() => {
        if (active) setFailed(true)
      })
    return () => {
      active = false
    }
  }, [attempt, stepUpMode])

  const verifyPassword = async () => {
    setPasswordPending(true)
    setFailed(false)
    try {
      enterWorkbenchAdmin(await requestAdminStepUpTicket('password', password))
    } catch {
      setFailed(true)
    } finally {
      setPasswordPending(false)
    }
  }

  if (stepUpMode) {
    return (
      <Main>
        <div className='mx-auto flex min-h-[360px] w-full max-w-md flex-col justify-center gap-4'>
          <div>
            <h1 className='text-xl font-semibold'>
              {t('Security verification')}
            </h1>
            <p className='text-muted-foreground mt-1 text-sm'>
              {t('Confirm your identity before changing Workbench settings.')}
            </p>
          </div>
          <Input
            type='password'
            autoComplete='current-password'
            value={password}
            onChange={(event) => setPassword(event.target.value)}
            placeholder={t('Password')}
            disabled={passwordPending}
          />
          <Button
            disabled={passwordPending || password === ''}
            onClick={verifyPassword}
          >
            {passwordPending ? (
              <Spinner className='size-4' />
            ) : (
              t('Verify with password')
            )}
          </Button>
          <Button
            variant='outline'
            onClick={() =>
              verification.startVerification(
                () => requestAdminStepUpTicket('secure_verification'),
                {
                  title: t('Security verification'),
                  description: t(
                    'Confirm your identity before changing Workbench settings.'
                  ),
                }
              )
            }
          >
            {t('Verify with 2FA or Passkey')}
          </Button>
          {failed && <ErrorState onRetry={verifyPassword} />}
        </div>
        <SecureVerificationDialog
          open={verification.open}
          onOpenChange={verification.setOpen}
          methods={verification.methods}
          state={verification.state}
          onVerify={async (method, code) => {
            await verification.executeVerification(method, code)
          }}
          onCancel={verification.cancel}
          onCodeChange={verification.setCode}
          onMethodChange={verification.switchMethod}
        />
      </Main>
    )
  }

  return (
    <Main>
      {failed ? (
        <ErrorState onRetry={() => setAttempt((value) => value + 1)} />
      ) : (
        <div className='flex min-h-[300px] items-center justify-center'>
          <Spinner className='size-6' />
        </div>
      )}
    </Main>
  )
}
