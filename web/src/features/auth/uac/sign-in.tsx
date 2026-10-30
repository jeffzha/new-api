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
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { useAuthRedirect } from '@/features/auth/hooks/use-auth-redirect'
import { api } from '@/lib/api'

import { instancePath } from './instance'

export function UACSignIn() {
  const { t } = useTranslation()
  const [leaving, setLeaving] = useState(false)
  const [completionError, setCompletionError] = useState(false)
  const completion = useRef<Promise<unknown> | null>(null)
  const { handleLoginResult } = useAuthRedirect()
  useEffect(() => {
    if (
      new URLSearchParams(window.location.search).get('uac_complete') !== '1'
    ) {
      return
    }
    let active = true
    completion.current ??= api.post('/api/oauth/uac/complete', undefined, {
      skipAuthRefresh: true,
      skipErrorHandler: true,
    })
    void completion.current
      .then(async (value) => {
        if (!active) return
        const response = value as { data: { success: boolean; data?: unknown } }
        if (!response.data.success) {
          setCompletionError(true)
          return
        }
        await handleLoginResult(response.data.data)
      })
      .catch(() => {
        if (active) setCompletionError(true)
      })
    return () => {
      active = false
    }
  }, [handleLoginResult])
  const error = new URLSearchParams(window.location.search).get('uac_error')
  return (
    <div className='space-y-5'>
      <p className='text-muted-foreground text-sm'>
        {t('Use your unified authentication account to access this platform.')}
      </p>
      {(error || completionError) && (
        <p role='alert' className='text-destructive text-sm'>
          {error === 'account'
            ? t(
                'Your local administrator account is unavailable. Contact the platform administrator.'
              )
            : t(
                'Unified sign-in failed. Try again or contact the platform administrator.'
              )}
        </p>
      )}
      <Button
        className='w-full'
        disabled={leaving}
        onClick={() => {
          setLeaving(true)
          window.location.assign(instancePath('/api/oauth/uac/start'))
        }}
      >
        {leaving
          ? t('Redirecting...')
          : t('Sign in with unified authentication')}
      </Button>
    </div>
  )
}
