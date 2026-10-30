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
import { useTranslation } from 'react-i18next'

import { Card, CardContent, CardHeader } from '@/components/ui/card'
import { useSystemConfig } from '@/hooks/use-system-config'

import '@/styles/uac-sign-in.css'

import { instanceAsset } from './instance'
import { UACSignIn } from './sign-in'

// Project presentation composes the existing UAC handoff. The product password
// form and the authentication/session logic remain shared and unchanged.
export function ProjectSignIn() {
  const { t } = useTranslation()
  const { systemName, logo } = useSystemConfig()

  return (
    <div className='uac-sign-in flex min-h-svh flex-col'>
      <header className='flex min-h-18 items-center border-b px-5 py-4 sm:px-9'>
        <div className='flex min-w-0 items-center gap-3'>
          <img
            src={instanceAsset(logo)}
            alt=''
            className='size-8 shrink-0 rounded-lg object-contain'
          />
          <span className='text-base font-semibold break-words'>
            {systemName}
          </span>
        </div>
      </header>
      <main className='flex flex-1 items-center justify-center px-4 py-12'>
        <Card className='w-full max-w-[480px] gap-0 rounded-2xl py-0 shadow-xl'>
          <CardHeader className='justify-items-center border-b px-6 pt-8 pb-0'>
            <h1 className='border-primary border-b-2 px-1 pb-4 text-base font-semibold'>
              {t('Unified sign-in')}
            </h1>
          </CardHeader>
          <CardContent className='flex flex-col gap-7 px-6 py-9 sm:px-10 sm:py-10'>
            <div className='flex flex-col gap-2'>
              <h2 className='text-xl font-semibold break-words'>
                {systemName}
              </h2>
              <p className='text-muted-foreground text-sm leading-6'>
                {t('Continue to the unified authentication center to sign in.')}
              </p>
            </div>
            <UACSignIn />
          </CardContent>
        </Card>
      </main>
      <footer className='text-muted-foreground px-5 pb-7 text-center text-xs leading-5'>
        {t('For account access, contact your platform administrator.')}
      </footer>
    </div>
  )
}
