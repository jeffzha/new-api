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
  ChevronRight,
  CreditCard,
  KeyRound,
  PlugZap,
} from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { AnimateInView } from '@/components/animate-in-view'
import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { useAuthStore } from '@/stores/auth-store'

export function HowItWorks() {
  const { t } = useTranslation()
  const user = useAuthStore((state) => state.auth.user)
  const [pendingStep, setPendingStep] = useState<{
    title: string
    path: string
  } | null>(null)

  const steps = [
    {
      num: '1',
      title: t('Recharge'),
      desc: t('Top up your balance for model usage.'),
      action: t('Go to recharge'),
      path: '/wallet' as const,
      icon: CreditCard,
    },
    {
      num: '2',
      title: t('Configure'),
      desc: t('Create an API key and set its permissions and quota.'),
      action: t('Configure API keys'),
      path: '/keys' as const,
      icon: KeyRound,
    },
    {
      num: '3',
      title: t('Connect'),
      desc: t('Connect your application using the API documentation.'),
      action: t('Open API documentation'),
      path: '/docs' as const,
      icon: PlugZap,
    },
  ]

  return (
    <section
      className='gateway-onboarding gateway-section'
      aria-labelledby='gateway-steps-title'
    >
      <div className='gateway-container'>
        <div className='gateway-section-heading'>
          <h2 id='gateway-steps-title'>{t('Three steps to get started')}</h2>
        </div>

        <ol className='gateway-steps'>
          {steps.map((step, i) => (
            <AnimateInView
              key={step.num}
              as='li'
              delay={i * 80}
              animation='fade-up'
              className='gateway-step'
            >
              <div className='gateway-step-top' aria-hidden='true'>
                <span className='gateway-step-icon'>
                  <step.icon size={19} strokeWidth={1.6} />
                </span>
                <span className='gateway-step-number'>0{step.num}</span>
                <span className='gateway-step-connector'>
                  <ChevronRight size={15} />
                </span>
              </div>
              <h3>{step.title}</h3>
              <p>{step.desc}</p>
              <Button
                variant='ghost'
                className='gateway-step-action'
                render={<Link to={step.path} />}
                onClick={(event) => {
                  if (!user) {
                    event.preventDefault()
                    setPendingStep({ title: step.title, path: step.path })
                  }
                }}
              >
                {step.action}
                <ArrowRight size={15} aria-hidden='true' />
              </Button>
            </AnimateInView>
          ))}
        </ol>
      </div>
      <Dialog
        open={!!pendingStep}
        onOpenChange={(open) => {
          if (!open) setPendingStep(null)
        }}
        title={t('Sign in required')}
        description={t('Please sign in to view {{module}}.', {
          module: pendingStep?.title ?? '',
        })}
        contentClassName='sm:max-w-md'
        contentHeight='auto'
        footer={
          <>
            <Button variant='outline' onClick={() => setPendingStep(null)}>
              {t('Cancel')}
            </Button>
            <Button
              render={
                <Link to='/sign-in' search={{ redirect: pendingStep?.path }} />
              }
            >
              {t('Sign in now')}
            </Button>
          </>
        }
      >
        {null}
      </Dialog>
    </section>
  )
}
