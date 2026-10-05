/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import {
  Building2,
  Check,
  FlaskConical,
  Grid2X2,
  PanelsTopLeft,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { useIsAdmin } from '@/hooks/use-admin'
import { getAgencyCenterUrl } from '@/lib/agency-center'
import { getModelMockUrl } from '@/lib/model-mock'

export function ProductSwitcher() {
  const { t } = useTranslation()
  const isAdmin = useIsAdmin()
  const modelMockUrl = getModelMockUrl()

  return (
    <DropdownMenu modal={false}>
      <DropdownMenuTrigger
        render={
          <Button
            variant='ghost'
            size='icon'
            className='size-8 shrink-0'
            aria-label={t('Products')}
          />
        }
      >
        <Grid2X2 className='size-4' aria-hidden />
      </DropdownMenuTrigger>
      <DropdownMenuContent align='start' sideOffset={8} className='w-52'>
        <DropdownMenuGroup>
          <DropdownMenuLabel>{t('Products')}</DropdownMenuLabel>
          <DropdownMenuItem disabled>
            <PanelsTopLeft aria-hidden />
            <span className='flex-1'>{t('Console')}</span>
            <Check aria-hidden />
          </DropdownMenuItem>
          <DropdownMenuItem render={<a href={getAgencyCenterUrl(true)} />}>
            <Building2 aria-hidden />
            <span>{t('Agency Center')}</span>
          </DropdownMenuItem>
          {isAdmin && modelMockUrl && (
            <DropdownMenuItem render={<a href={modelMockUrl} />}>
              <FlaskConical aria-hidden />
              <span>{t('Model mock scheduling')}</span>
            </DropdownMenuItem>
          )}
        </DropdownMenuGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
