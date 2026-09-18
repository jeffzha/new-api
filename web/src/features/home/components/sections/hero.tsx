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
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { useStatus } from '@/hooks/use-status'

import { HeroTerminalDemo } from '../hero-terminal-demo'

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
    <section className='border-border bg-background relative isolate overflow-hidden border-b px-6 pt-16 pb-14 md:pt-24 md:pb-20'>
      <div className='pointer-events-none absolute inset-0 -z-10 bg-[radial-gradient(circle_at_15%_15%,color-mix(in_oklab,var(--primary)_12%,transparent),transparent_34%),radial-gradient(circle_at_88%_20%,color-mix(in_oklab,var(--primary)_8%,transparent),transparent_30%)]' />
      <div className='mx-auto max-w-6xl'>
        <div className='grid gap-12 lg:grid-cols-[1fr_0.9fr] lg:items-center lg:gap-16'>
          <div>
            <p className='text-primary mb-4 text-sm font-semibold tracking-wide'>
              Nexus Reach AI Gateway
            </p>
            <h1 className='text-foreground max-w-3xl text-4xl leading-[1.08] font-semibold tracking-tight md:text-6xl'>
              {t('Unified API Gateway for')} {t('AI models')}
            </h1>
            <p className='text-muted-foreground mt-6 max-w-2xl text-base leading-7 md:text-lg'>
              {t(
                'Connect mainstream model services and compatible protocols with unified billing and usage management'
              )}
            </p>
            <div className='mt-8 flex flex-wrap gap-3'>
              <Button
                className='h-11 px-5'
                render={
                  <Link to={isAuthenticated ? '/dashboard' : '/sign-up'} />
                }
              >
                {isAuthenticated ? t('Go to Dashboard') : t('Get Started')}
                <ArrowRight className='ml-2 size-4' />
              </Button>
              <Button
                variant='outline'
                className='h-11 px-5'
                render={<Link to='/pricing' />}
              >
                {t('View Pricing')}
              </Button>
              <Button
                variant='ghost'
                className='h-11 px-4'
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

          <div className='min-w-0'>
            <HeroTerminalDemo />
          </div>
        </div>

        <div className='mt-14 grid gap-3 md:grid-cols-3'>
          {capabilities.map(({ icon: Icon, title, description }) => (
            <div
              key={title}
              className='border-border/60 bg-card/50 hover:bg-card flex gap-4 rounded-xl border p-5 transition-colors'
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
