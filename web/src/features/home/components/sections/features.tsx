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
    <section
      className='gateway-reasons gateway-section'
      aria-labelledby='gateway-reasons-title'
    >
      <div className='gateway-container'>
        <div className='gateway-section-heading'>
          <h2 id='gateway-reasons-title'>{t('Why choose us')}</h2>
          <span className='gateway-reasons-subtitle'>
            {t('Professional, reliable, efficient AI infrastructure')}
          </span>
        </div>

        <div className='gateway-reasons-grid'>
          {features.map((feature, index) => (
            <AnimateInView
              key={feature.title}
              delay={index * 70}
              animation='fade-up'
              className='gateway-reason'
            >
              <div className='gateway-reason-icon'>
                <feature.icon size={18} strokeWidth={1.7} aria-hidden='true' />
              </div>
              <div>
                <h3>{feature.title}</h3>
                <p>{feature.description}</p>
              </div>
            </AnimateInView>
          ))}
        </div>

        <div className='gateway-capabilities'>
          {platformCapabilities.map((feature) => (
            <div key={feature.title} className='gateway-capability'>
              <feature.icon size={17} strokeWidth={1.7} aria-hidden='true' />
              <div>
                <h3>{feature.title}</h3>
                <p>{feature.description}</p>
              </div>
            </div>
          ))}
        </div>
      </div>
    </section>
  )
}
