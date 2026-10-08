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
import { Activity, BarChart3, WalletCards } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { IconBadge, type IconBadgeTone } from '@/components/ui/icon-badge'
import { Skeleton } from '@/components/ui/skeleton'
import { formatQuota } from '@/lib/format'

import type { UserWalletData } from '../types'

const balanceLabelKeys: Record<string, string> = {
  recharge: 'Recharge balance',
  gift: 'Gift balance',
  redemption: 'Redemption code balance',
  other: 'Other balance',
}

interface WalletStatsCardProps {
  user: UserWalletData | null
  loading?: boolean
  workspace?: boolean
}

export function WalletStatsCard(props: WalletStatsCardProps) {
  const { t } = useTranslation()
  if (props.loading) {
    return (
      <div
        className={
          props.workspace
            ? 'console-wallet-balance'
            : 'grid grid-cols-2 rounded-lg border sm:grid-cols-[minmax(0,2fr)_minmax(0,1fr)_minmax(0,1fr)]'
        }
      >
        {['balance', 'usage', 'requests'].map((key, index) => {
          let loadingClassName = 'min-w-0 px-2.5 py-2.5 sm:px-5 sm:py-4'
          if (!props.workspace && index === 0) {
            loadingClassName =
              'col-span-2 min-w-0 border-b px-2.5 py-2.5 sm:col-span-1 sm:border-r sm:border-b-0 sm:px-5 sm:py-4'
          } else if (!props.workspace && index === 1) {
            loadingClassName += ' border-r'
          }

          return (
            <div
              key={key}
              className={loadingClassName}
              aria-label={t('Loading...')}
            >
              <Skeleton className='h-3.5 w-full' />
              <Skeleton className='mt-2 h-6 w-full sm:h-7' />
              <Skeleton className='mt-1.5 hidden h-3.5 w-24 md:block' />
            </div>
          )
        })}
      </div>
    )
  }

  const stats: {
    label: string
    value: string
    description: string
    icon: typeof WalletCards
    tone: IconBadgeTone
  }[] = [
    {
      label: t('Current Balance'),
      value: formatQuota(props.user?.quota ?? 0),
      description: t('Remaining quota'),
      icon: WalletCards,
      tone: 'success',
    },
    {
      label: t('Total Usage'),
      value: formatQuota(props.user?.used_quota ?? 0),
      description: t('Total consumed quota'),
      icon: BarChart3,
      tone: 'info',
    },
    {
      label: t('API Requests'),
      value: (props.user?.request_count ?? 0).toLocaleString(),
      description: t('Total requests made'),
      icon: Activity,
      tone: 'chart-4',
    },
  ]
  const balanceBreakdown = props.user?.wallet_balances ?? []

  const breakdown = balanceBreakdown.length > 0 && (
    <dl
      role='group'
      aria-label={t('Balance breakdown')}
      className='wallet-balance-breakdown text-muted-foreground mt-2.5 grid grid-cols-1 gap-x-4 gap-y-1.5 text-xs font-normal sm:grid-cols-2'
    >
      {balanceBreakdown.map((balance) => (
        <div
          key={balance.type}
          className='wallet-balance-breakdown-item flex min-w-0 justify-between gap-2'
        >
          <dt className='min-w-0 break-words'>
            {balanceLabelKeys[balance.type]
              ? t(balanceLabelKeys[balance.type])
              : t('Balance category', { type: balance.type })}
          </dt>
          <dd className='text-foreground shrink-0 font-mono tabular-nums'>
            {formatQuota(balance.quota)}
          </dd>
        </div>
      ))}
    </dl>
  )

  if (props.workspace) {
    return (
      <dl className='console-wallet-balance'>
        {stats.map((item, index) => (
          <div key={item.label} className='console-wallet-stat'>
            <dt className='console-wallet-stat-label'>
              <IconBadge tone={item.tone} size='sm'>
                <item.icon aria-hidden />
              </IconBadge>
              {item.label}
            </dt>
            <dd className='console-wallet-stat-value'>
              {props.user ? item.value : '-'}
              {index === 0 && breakdown}
              {index !== 0 && (
                <span className='console-wallet-stat-note'>
                  {item.description}
                </span>
              )}
            </dd>
          </div>
        ))}
      </dl>
    )
  }

  return (
    <div className='grid grid-cols-2 rounded-lg border sm:grid-cols-[minmax(0,2fr)_minmax(0,1fr)_minmax(0,1fr)]'>
      {stats.map((item, index) => (
        <div
          key={item.label}
          className={
            index === 0
              ? 'col-span-2 min-w-0 border-b px-2.5 py-2.5 sm:col-span-1 sm:border-r sm:border-b-0 sm:px-5 sm:py-4'
              : `min-w-0 px-2.5 py-2.5 sm:px-5 sm:py-4 ${index === 1 ? 'border-r' : ''}`
          }
        >
          <div className='flex items-center gap-1.5 sm:gap-2.5'>
            <IconBadge tone={item.tone} size='stat'>
              <item.icon />
            </IconBadge>
            <div className='text-muted-foreground truncate text-[11px] font-medium tracking-wider uppercase sm:text-xs'>
              {item.label}
            </div>
          </div>

          <div className='text-foreground mt-1.5 font-mono text-sm font-bold tracking-tight break-all tabular-nums sm:mt-2.5 sm:text-2xl'>
            {props.user ? item.value : '-'}
          </div>
          {index === 0 ? (
            breakdown
          ) : (
            <div className='text-muted-foreground/60 mt-1 hidden text-xs md:block'>
              {item.description}
            </div>
          )}
        </div>
      ))}
    </div>
  )
}
