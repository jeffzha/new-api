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
import { useMutation, useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'
import { Main } from '@/components/layout'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Spinner } from '@/components/ui/spinner'
import { api } from '@/lib/api'

import { requireLocalSSORedirect } from './selection-redirect'
import {
  isWorkbenchCustomerRole,
  selectableWorkbenchSelectionToken,
  workbenchRoleLabelKey,
} from './workbench-role'

interface WorkbenchSelectionOption {
  selection_token: string
  expires_at: string
  customer_code: string
  customer_display_name: string
  role: string
  app_selector: string
  app_alias: string
  app_display_name: string
  app_status: string
  is_default: boolean
  access_mode: string
}

interface SelectionListResponse {
  success: boolean
  data?: WorkbenchSelectionOption[]
}

interface SelectionResultResponse {
  success: boolean
  data?: {
    redirect_url?: string
    expires_at?: string
  }
}

export function WorkbenchContextSelection() {
  const { t } = useTranslation()
  const selections = useQuery({
    queryKey: ['workbench-context-selections'],
    queryFn: async () => {
      const response = await api.get<SelectionListResponse>(
        '/api/workbench/selections',
        { skipBusinessError: true, skipErrorHandler: true }
      )
      if (!response.data.success || response.data.data == null) {
        throw new Error('Invalid workbench selection response')
      }
      return response.data.data
    },
    retry: false,
    staleTime: 0,
  })
  const chooseSelection = useMutation({
    mutationFn: async (selectionToken: string) => {
      const response = await api.post<SelectionResultResponse>(
        '/api/workbench/selections/choose',
        { selection_token: selectionToken },
        { skipBusinessError: true, skipErrorHandler: true }
      )
      if (!response.data.success) {
        throw new Error('Invalid workbench selection result')
      }
      return requireLocalSSORedirect(
        response.data.data?.redirect_url,
        window.location.origin
      )
    },
    onSuccess: (redirectURL) => {
      // SSO establishes the isolated ADP session, so this must be a full-page
      // navigation rather than a client-side router transition.
      window.location.replace(redirectURL)
    },
  })

  if (selections.isPending) {
    return (
      <Main>
        <div className='flex min-h-[300px] items-center justify-center'>
          <Spinner className='size-6' />
        </div>
      </Main>
    )
  }

  if (selections.isError || selections.data.length === 0) {
    return (
      <Main>
        <ErrorState
          description={t('Your workbench contexts could not be loaded.')}
          onRetry={() => void selections.refetch()}
          action={
            <Button
              variant='outline'
              size='sm'
              render={<Link to='/playground' />}
            >
              {t('Restart workbench sign-in')}
            </Button>
          }
        />
      </Main>
    )
  }

  return (
    <Main>
      <section
        className='mx-auto flex w-full max-w-3xl flex-col gap-6 py-4 sm:py-8'
        aria-labelledby='workbench-context-heading'
      >
        <header className='space-y-2'>
          <h1
            id='workbench-context-heading'
            className='text-2xl font-semibold tracking-tight'
          >
            {t('Choose workbench context')}
          </h1>
          <p className='text-muted-foreground'>
            {t('Select the customer and app you want to use for this session.')}
          </p>
        </header>

        {chooseSelection.isError && (
          <div
            role='alert'
            className='border-destructive/40 bg-destructive/5 text-destructive rounded-lg border px-4 py-3 text-sm'
          >
            {t('This context could not be opened. Reload it and try again.')}
          </div>
        )}

        <div className='grid gap-4 sm:grid-cols-2'>
          {selections.data.map((option) => {
            const supportedRole = isWorkbenchCustomerRole(option.role)
            const selectionToken = selectableWorkbenchSelectionToken(
              option.role,
              option.selection_token
            )
            return (
              <Card
                key={`${option.customer_code}:${option.app_selector}`}
                aria-disabled={!supportedRole}
              >
                <CardHeader>
                  <div className='flex flex-wrap items-center gap-2'>
                    <CardTitle>{option.customer_display_name}</CardTitle>
                    {option.is_default && (
                      <Badge variant='secondary'>{t('Default')}</Badge>
                    )}
                  </div>
                  <CardDescription>{option.app_display_name}</CardDescription>
                </CardHeader>
                <CardContent className='flex flex-wrap gap-2'>
                  <Badge variant={supportedRole ? 'outline' : 'destructive'}>
                    {t(workbenchRoleLabelKey(option.role))}
                  </Badge>
                  <Badge variant='outline'>
                    {accessModeLabel(t, option.access_mode)}
                  </Badge>
                  {!supportedRole && (
                    <p className='text-destructive w-full text-sm' role='alert'>
                      {t(
                        'This workbench context uses an unsupported role and cannot be opened.'
                      )}
                    </p>
                  )}
                </CardContent>
                <CardFooter>
                  <Button
                    className='w-full'
                    disabled={chooseSelection.isPending || !supportedRole}
                    onClick={() => {
                      if (selectionToken != null) {
                        chooseSelection.mutate(selectionToken)
                      }
                    }}
                  >
                    {chooseSelection.isPending && <Spinner />}
                    {t('Open workbench')}
                  </Button>
                </CardFooter>
              </Card>
            )
          })}
        </div>
      </section>
    </Main>
  )
}

function accessModeLabel(
  t: (key: string) => string,
  accessMode: string
): string {
  switch (accessMode) {
    case 'active':
      return t('Full access')
    case 'readonly':
      return t('Read-only access')
    default:
      return t('Suspended access')
  }
}
