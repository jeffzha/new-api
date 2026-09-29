/* Copyright (C) 2023-0-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { Crown, WalletCards } from 'lucide-react'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useMediaQuery } from '@/hooks'

export function WalletOperations(props: {
  workspace: boolean
  hasSubscriptions: boolean
  funds: ReactNode
  subscriptions: ReactNode
}) {
  const { t } = useTranslation()
  const compact = useMediaQuery('(max-width: 767px)')
  if (!props.workspace) {
    return (
      <div
        className={
          props.hasSubscriptions
            ? 'grid gap-4 xl:grid-cols-[minmax(0,1.05fr)_minmax(360px,0.95fr)] xl:items-start'
            : 'grid gap-4'
        }
      >
        <div id='wallet-add-funds' className='scroll-mt-4'>
          {props.funds}
        </div>
        {props.subscriptions}
      </div>
    )
  }
  return (
    <Tabs
      defaultValue='funds'
      orientation={compact ? 'horizontal' : 'vertical'}
      className='console-settings console-wallet-operations'
    >
      <TabsList variant='line' aria-label={t('Wallet')}>
        <TabsTrigger value='funds'>
          <WalletCards aria-hidden />
          {t('Add Funds')}
        </TabsTrigger>
        <TabsTrigger value='plans'>
          <Crown aria-hidden />
          {t('Subscriptions')}
        </TabsTrigger>
      </TabsList>
      <TabsContent
        value='funds'
        keepMounted
        id='wallet-add-funds'
        className='m-0 min-h-0 min-w-0 overflow-y-auto'
      >
        {props.funds}
      </TabsContent>
      <TabsContent
        value='plans'
        keepMounted
        className='m-0 min-h-0 min-w-0 overflow-y-auto'
      >
        {!props.hasSubscriptions && (
          <p className='text-muted-foreground py-8 text-sm'>
            {t('No subscription plans available')}
          </p>
        )}
        {props.subscriptions}
      </TabsContent>
    </Tabs>
  )
}
