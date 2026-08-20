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
import { Link } from '@tanstack/react-router'
import axios from 'axios'
import { lazy, Suspense, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'
import { Main } from '@/components/layout'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'
import { api } from '@/lib/api'

import { workbenchEntryFailureState } from './entry-fallback'

const LegacyPlayground = lazy(() =>
  import('@/features/playground').then((module) => ({
    default: module.Playground,
  }))
)

type EntryState = 'loading' | 'legacy' | 'error'

interface SessionTicketResponse {
  success: boolean
  data?: {
    ticket?: string
  }
}

let pendingSessionTicket: Promise<string> | undefined

function requestSessionTicket(): Promise<string> {
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

export function WorkbenchEntry() {
  const { t } = useTranslation()
  const [entryState, setEntryState] = useState<EntryState>('loading')
  const [attempt, setAttempt] = useState(0)

  useEffect(() => {
    let active = true
    setEntryState('loading')

    void requestSessionTicket()
      .then((ticket) => {
        if (!active) return
        const target = new URL('/api/workbench/entry', window.location.origin)
        target.searchParams.set('ticket', ticket)
        window.location.replace(target.toString())
      })
      .catch((error: unknown) => {
        if (!active) return
        const responseStatus = axios.isAxiosError(error)
          ? error.response?.status
          : undefined
        setEntryState(workbenchEntryFailureState(responseStatus))
      })

    return () => {
      active = false
    }
  }, [attempt])

  if (entryState === 'legacy') {
    return (
      <Main className='p-0'>
        <Suspense fallback={<WorkbenchLoading />}>
          <LegacyPlayground />
        </Suspense>
      </Main>
    )
  }

  if (entryState === 'error') {
    return (
      <Main>
        <ErrorState
          onRetry={() => setAttempt((value) => value + 1)}
          action={
            <Button
              variant='outline'
              size='sm'
              render={<Link to='/playground/legacy' />}
            >
              {t('Playground')}
            </Button>
          }
        />
      </Main>
    )
  }

  return (
    <Main>
      <WorkbenchLoading />
    </Main>
  )
}

function WorkbenchLoading() {
  return (
    <div className='flex min-h-[300px] items-center justify-center'>
      <Spinner className='size-6' />
    </div>
  )
}
