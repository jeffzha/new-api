/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import {
  Check,
  KeyRound,
  Network,
  ShieldCheck,
  WalletCards,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { AnimateInView } from '@/components/animate-in-view'
import { getLobeIcon } from '@/lib/lobe-icon'

export function PlatformValue() {
  const { t } = useTranslation()
  const values = [
    {
      id: 'access',
      icon: Network,
      title: t('One-stop access'),
      description: t(
        'One API key lets you call multiple mainstream models through unified access and management, eliminating the cost of integrating with multiple providers.'
      ),
      visual: (
        <div className='value-access-visual' aria-hidden='true'>
          <div className='value-key'>
            <KeyRound size={18} />
            <span>API KEY</span>
          </div>
          <div className='value-model-icons'>
            {['OpenAI', 'Claude.Color', 'DeepSeek.Color', 'Gemini.Color'].map(
              (icon) => (
                <span key={icon}>{getLobeIcon(icon, 24)}</span>
              )
            )}
          </div>
        </div>
      ),
    },
    {
      id: 'reliability',
      icon: ShieldCheck,
      title: t('High availability'),
      description: t(
        'Intelligent routing automatically selects the best route and maintains upstream redundancy, switching away from single-point failures in milliseconds.'
      ),
      visual: (
        <div className='value-routing-visual' aria-hidden='true'>
          <span className='value-routing-line'>
            <i />
            <i />
            <i />
            <i />
            <i />
            <i />
            <i />
            <i />
          </span>
          <span className='value-route'>
            <span>ROUTE A</span>
            <Check size={14} />
          </span>
          <span className='value-route value-route-secondary'>
            <span>ROUTE B</span>
            <ShieldCheck size={14} />
          </span>
        </div>
      ),
    },
    {
      id: 'budget',
      icon: WalletCards,
      title: t('Fine-grained cost control'),
      description: t(
        'Set limits and quotas by department, user, or API key with transparent usage and predictable costs.'
      ),
      visual: (
        <div className='value-budget-visual' aria-hidden='true'>
          <div>
            <span>{t('Department')}</span>
            <i>
              <b />
            </i>
          </div>
          <div>
            <span>{t('User')}</span>
            <i>
              <b />
            </i>
          </div>
          <div>
            <span>API KEY</span>
            <i>
              <b />
            </i>
          </div>
        </div>
      ),
    },
  ]

  return (
    <section
      className='gateway-value gateway-section'
      aria-labelledby='gateway-value-title'
    >
      <div className='gateway-container'>
        <div className='gateway-section-heading'>
          <h2 id='gateway-value-title'>{t('Platform value')}</h2>
          <span className='gateway-section-index' aria-hidden='true'>
            PLATFORM VALUE
          </span>
        </div>
        <div className='gateway-value-grid'>
          {values.map((value, index) => (
            <AnimateInView
              key={value.id}
              delay={index * 80}
              className={`gateway-value-card gateway-value-${value.id}`}
            >
              {value.visual}
              <div className='gateway-value-title'>
                <value.icon size={18} aria-hidden='true' />
                <h3>{value.title}</h3>
              </div>
              <p>{value.description}</p>
            </AnimateInView>
          ))}
        </div>
      </div>
    </section>
  )
}
