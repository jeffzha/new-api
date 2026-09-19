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
import {
  Blocks,
  ChartNoAxesCombined,
  Code2,
  Gauge,
  Headphones,
  Network,
  ShieldCheck,
  UsersRound,
  WalletCards,
  Zap,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { AnimateInView } from '@/components/animate-in-view'

interface FeaturesProps {
  className?: string
}

export function Features(_props: FeaturesProps) {
  const { t } = useTranslation()
  const features = [
    {
      icon: Zap,
      title: t('Lightning performance'),
      description: t(
        'Optimized network architecture ensures millisecond response times'
      ),
    },
    {
      icon: ShieldCheck,
      title: t('Enterprise security'),
      description: t(
        'End-to-end protection with comprehensive permission management'
      ),
    },
    {
      icon: Network,
      title: t('Unified model access'),
      description: t(
        'Connect mainstream model services and compatible protocols with unified billing and usage management'
      ),
    },
    {
      icon: Code2,
      title: t('Developer-friendly integration'),
      description: t(
        'Complete API documentation with multi-language SDK support'
      ),
    },
    {
      icon: ChartNoAxesCombined,
      title: t('Real-time monitoring'),
      description: t(
        'Track usage, costs and performance with real-time analytics'
      ),
    },
    {
      icon: Headphones,
      title: t('24/7 support'),
      description: t(
        'Professional technical support with fast response around the clock'
      ),
    },
  ]
  const platformCapabilities = [
    {
      icon: Gauge,
      title: t('High Performance'),
      description: t(
        'Support for high concurrency with automatic load balancing'
      ),
    },
    {
      icon: WalletCards,
      title: t('Transparent Billing'),
      description: t('Pay-as-you-go with real-time usage monitoring'),
    },
    {
      icon: UsersRound,
      title: t('Team Collaboration'),
      description: t(
        'Multi-user management with flexible permission allocation'
      ),
    },
    {
      icon: Blocks,
      title: t('Open-source project'),
      description: t('Community-driven, self-hostable, and easy to extend'),
    },
  ]

  return (
    <section className='bg-muted/20 relative z-10 px-5 py-20 sm:px-6 md:py-28'>
      <div className='mx-auto max-w-6xl'>
        <AnimateInView className='mx-auto mb-12 max-w-2xl text-center'>
          <p className='text-primary mb-3 text-xs font-bold tracking-[0.14em] uppercase'>
            {t('Why choose us')}
          </p>
          <h2 className='text-foreground text-3xl font-bold tracking-normal md:text-4xl'>
            {t('Professional, reliable, efficient AI infrastructure')}
          </h2>
        </AnimateInView>

        <div className='grid gap-4 md:grid-cols-2 lg:grid-cols-3'>
          {features.map((feature, index) => (
            <AnimateInView
              key={feature.title}
              delay={index * 70}
              animation='fade-up'
              className='border-border/70 bg-card hover:border-primary/30 min-h-48 rounded-lg border p-6 shadow-sm transition-[border-color,transform,box-shadow] duration-200 hover:-translate-y-0.5 hover:shadow-md'
            >
              <div className='bg-primary text-primary-foreground mb-6 flex size-10 items-center justify-center rounded-md shadow-sm'>
                <feature.icon className='size-5' strokeWidth={1.8} />
              </div>
              <h3 className='text-base font-semibold'>{feature.title}</h3>
              <p className='text-muted-foreground mt-2 text-sm leading-6'>
                {feature.description}
              </p>
            </AnimateInView>
          ))}
        </div>

        <div className='border-border/70 bg-card mt-12 grid overflow-hidden rounded-lg border sm:grid-cols-2 lg:grid-cols-4'>
          {platformCapabilities.map((feature) => (
            <div
              key={feature.title}
              className='border-border/60 flex gap-4 border-b p-5 last:border-b-0 lg:border-r lg:border-b-0 lg:last:border-r-0 sm:[&:nth-child(3)]:border-b-0 sm:[&:nth-child(odd)]:border-r'
            >
              <div className='text-primary bg-primary/10 flex size-9 shrink-0 items-center justify-center rounded-md'>
                <feature.icon className='size-4.5' strokeWidth={1.8} />
              </div>
              <div>
                <h3 className='text-sm font-semibold'>{feature.title}</h3>
                <p className='text-muted-foreground mt-1 text-xs leading-5'>
                  {feature.description}
                </p>
              </div>
            </div>
          ))}
        </div>
      </div>
    </section>
  )
}
