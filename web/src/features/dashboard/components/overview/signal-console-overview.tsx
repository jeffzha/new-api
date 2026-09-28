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
import { ArrowRight, KeyRound, RadioTower, RotateCcw } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { SectionPageLayout } from '@/components/layout'
import { Button } from '@/components/ui/button'
import { useThemeCustomization } from '@/context/theme-customization-provider'
import { formatNumber, formatQuota } from '@/lib/format'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { useDashboardContentVisibility } from '../../hooks/use-status-data'
import { AnnouncementsPanel } from './announcements-panel'
import { ApiInfoPanel } from './api-info-panel'
import { FAQPanel } from './faq-panel'
import { PerformanceHealthPanel } from './performance-health-panel'
import { SignalUsagePanel } from './signal-usage-panel'
import { UptimePanel } from './uptime-panel'

export function SignalConsoleOverview() {
  const { t } = useTranslation()
  const user = useAuthStore((state) => state.auth.user)
  const { setPreset } = useThemeCustomization()
  const visibility = useDashboardContentVisibility()
  const isAdmin = (user?.role ?? 0) >= ROLE.ADMIN
  const actions = [
    { to: '/keys', title: t('API Keys'), description: t('signal.keysHint') },
    {
      to: '/playground',
      title: t('Playground'),
      description: t('signal.playgroundHint'),
    },
    {
      to: '/usage-logs',
      title: t('Usage logs'),
      description: t('signal.logsHint'),
    },
    {
      to: '/pricing',
      title: t('Model Square'),
      description: t('signal.pricingHint'),
    },
  ] as const
  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>{t('Overview')}</SectionPageLayout.Title>
      <SectionPageLayout.Actions>
        <Button variant='ghost' size='sm' onClick={() => setPreset('default')}>
          <RotateCcw aria-hidden='true' />
          {t('signal.restore')}
        </Button>
      </SectionPageLayout.Actions>
      <SectionPageLayout.Content>
        <div
          className='signal-overview flex min-w-0 flex-col gap-6'
          data-testid='signal-overview'
        >
          <header className='flex flex-wrap items-center justify-between gap-4'>
            <div className='flex flex-col gap-2'>
              <p className='text-primary flex items-center gap-2 text-xs font-semibold tracking-widest uppercase'>
                <RadioTower className='size-4' aria-hidden='true' />
                {t('preset.signal-console')}
              </p>
              <h3 className='text-2xl font-semibold tracking-tight sm:text-3xl'>
                {t('signal.title')}
              </h3>
              <p className='text-muted-foreground text-sm'>
                {t('signal.subtitle')}
              </p>
            </div>
            <Button render={<Link to='/keys' />} className='min-h-11'>
              <KeyRound aria-hidden='true' />
              {t('Create API Key')}
            </Button>
          </header>
          <div className='grid min-w-0 items-stretch gap-5 xl:grid-cols-[minmax(0,1fr)_19rem]'>
            <SignalUsagePanel />
            <aside
              className='signal-account relative flex min-w-0 flex-col overflow-hidden rounded-2xl p-6'
              aria-label={t('signal.account')}
            >
              <div className='signal-orbit' aria-hidden='true' />
              <div className='relative flex flex-1 flex-col gap-6'>
                <h3 className='text-sm font-medium'>{t('signal.account')}</h3>
                <div className='flex flex-col gap-2'>
                  <p className='signal-account-muted text-xs'>
                    {t('Credit remaining')}
                  </p>
                  <p className='text-3xl font-semibold tracking-tight break-words tabular-nums'>
                    {user ? formatQuota(user.quota ?? 0) : '—'}
                  </p>
                  <p className='signal-account-muted text-xs'>
                    {t('signal.balanceHint')}
                  </p>
                </div>
                <dl className='signal-account-totals mt-auto grid gap-4 pt-5'>
                  <div className='flex flex-wrap justify-between gap-2 text-sm'>
                    <dt className='signal-account-muted'>
                      {t('signal.lifetimeUsage')}
                    </dt>
                    <dd className='font-medium tabular-nums'>
                      {user ? formatQuota(user.used_quota ?? 0) : '—'}
                    </dd>
                  </div>
                  <div className='flex flex-wrap justify-between gap-2 text-sm'>
                    <dt className='signal-account-muted'>
                      {t('signal.lifetimeRequests')}
                    </dt>
                    <dd className='font-medium tabular-nums'>
                      {user ? formatNumber(user.request_count ?? 0) : '—'}
                    </dd>
                  </div>
                </dl>
                <Button
                  variant='secondary'
                  render={<Link to='/wallet' />}
                  className='min-h-11 justify-between'
                >
                  {t('Wallet')}
                  <ArrowRight aria-hidden='true' />
                </Button>
              </div>
            </aside>
          </div>
          <nav
            className='signal-shortcuts grid grid-cols-1 overflow-hidden rounded-xl border sm:grid-cols-2 xl:grid-cols-4'
            aria-label={t('Quick actions')}
          >
            {actions.map((action, index) => (
              <Button
                key={action.to}
                variant='ghost'
                render={<Link to={action.to} />}
                className='h-auto min-h-24 justify-start gap-3 rounded-none px-5 py-4 whitespace-normal'
              >
                <span
                  className='text-primary font-mono text-xs'
                  aria-hidden='true'
                >
                  0{index + 1}
                </span>
                <span className='flex min-w-0 flex-1 flex-col items-start gap-1 text-left'>
                  <span className='font-semibold'>{action.title}</span>
                  <span className='text-muted-foreground text-xs font-normal'>
                    {action.description}
                  </span>
                </span>
                <ArrowRight
                  className='text-muted-foreground size-4 shrink-0'
                  aria-hidden='true'
                />
              </Button>
            ))}
          </nav>
          {isAdmin && <PerformanceHealthPanel />}
          {(visibility.apiInfo || visibility.uptimeKuma) && (
            <div className='grid min-w-0 gap-5 lg:grid-cols-2'>
              {visibility.apiInfo && <ApiInfoPanel />}
              {visibility.uptimeKuma && <UptimePanel />}
            </div>
          )}
          {(visibility.announcements || visibility.faq) && (
            <div className='grid min-w-0 gap-5 lg:grid-cols-2'>
              {visibility.announcements && <AnnouncementsPanel />}
              {visibility.faq && <FAQPanel />}
            </div>
          )}
        </div>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
