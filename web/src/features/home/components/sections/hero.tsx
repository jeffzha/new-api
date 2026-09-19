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
import {
  ArrowRight,
  BookOpen,
  CheckCircle2,
  ShieldCheck,
  WalletCards,
} from 'lucide-react'
import { Trans, useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { useStatus } from '@/hooks/use-status'

import { HeroTerminalDemo } from '../hero-terminal-demo'
import { Stats } from './stats'

interface HeroProps {
  className?: string
  isAuthenticated?: boolean
}

export function Hero({ isAuthenticated }: HeroProps) {
  const { t } = useTranslation()
  const { status } = useStatus()
  const docsUrl = (status?.docs_link as string | undefined) || '/docs'
  const capabilities = [
    {
      icon: ShieldCheck,
      title: t('Secure & Reliable'),
      description: t(
        'Enterprise-grade security with comprehensive permission management'
      ),
    },
    {
      icon: WalletCards,
      title: t('Transparent Billing'),
      description: t('Pay-as-you-go with real-time usage monitoring'),
    },
    {
      icon: CheckCircle2,
      title: t('Protocol Compatible'),
      description: t(
        'Connect mainstream model services and compatible protocols with unified billing and usage management'
      ),
    },
  ]

  return (
    <section className='border-border bg-background relative isolate overflow-hidden border-b px-5 pt-14 pb-16 sm:px-6 md:pt-20 md:pb-24'>
      <div className='pointer-events-none absolute inset-x-0 top-0 -z-10 h-80 bg-[linear-gradient(180deg,color-mix(in_oklab,var(--primary)_7%,transparent),transparent)]' />
      <div className='mx-auto max-w-6xl'>
        <div className='max-w-5xl'>
          <p className='text-primary mb-5 text-xs font-bold tracking-[0.14em] uppercase md:text-sm'>
            {t('Enterprise AI MaaS platform')}
          </p>
          <h1 className='text-foreground max-w-5xl text-[2.6rem] leading-[1.06] font-bold tracking-normal sm:text-5xl md:text-[4rem]'>
            <Trans
              i18nKey='One API for 200+ mainstream models'
              components={{ highlight: <span className='text-primary' /> }}
            />
          </h1>
          <p className='text-muted-foreground mt-6 max-w-3xl text-base leading-7 md:text-lg'>
            {t(
              'Enterprise-grade AI orchestration hub for reliable, cost-controlled agents'
            )}
          </p>
          <div className='mt-8 flex flex-wrap gap-3'>
            <Button
              className='h-11 rounded-md px-6 shadow-sm'
              render={<Link to={isAuthenticated ? '/dashboard' : '/sign-up'} />}
            >
              {isAuthenticated ? t('Go to Dashboard') : t('Get Started')}
              <ArrowRight className='ml-2 size-4' />
            </Button>
            <Button
              variant='outline'
              className='h-11 rounded-md px-6'
              render={<Link to='/pricing' />}
            >
              {t('View Pricing')}
            </Button>
            <Button
              variant='ghost'
              className='h-11 rounded-md px-5'
              render={
                docsUrl.startsWith('http') ? (
                  <a href={docsUrl} target='_blank' rel='noreferrer' />
                ) : (
                  <Link to={docsUrl} />
                )
              }
            >
              <BookOpen className='mr-2 size-4' />
              {t('Docs')}
            </Button>
          </div>
        </div>

        <Stats />

        <div className='mt-5 min-w-0 rounded-xl border border-blue-200/70 bg-blue-50/70 p-2.5 shadow-[0_20px_60px_-34px_rgba(37,99,235,0.45)] sm:p-3 dark:border-blue-900/60 dark:bg-blue-950/30'>
          <HeroTerminalDemo className='max-w-none' />
        </div>

        <div className='mt-5 grid gap-3 md:grid-cols-3'>
          {capabilities.map(({ icon: Icon, title, description }) => (
            <div
              key={title}
              className='border-border/70 bg-card hover:border-primary/30 flex gap-4 rounded-lg border p-5 transition-colors'
            >
              <Icon
                className='text-primary mt-0.5 size-5 shrink-0'
                aria-hidden='true'
              />
              <div>
                <h2 className='text-sm font-semibold'>{title}</h2>
                <p className='text-muted-foreground mt-1 text-sm leading-6'>
                  {description}
                </p>
              </div>
            </div>
          ))}
        </div>
      </div>
    </section>
  )
}
