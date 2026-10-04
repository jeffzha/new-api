/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { Link } from '@tanstack/react-router'
import { ArrowUpRight } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import {
  Accordion,
  AccordionContent,
  AccordionItem,
  AccordionTrigger,
} from '@/components/ui/accordion'
import { Button } from '@/components/ui/button'

export function HomeFaq() {
  const { t } = useTranslation()
  const questions = [
    {
      question: t('What does this platform provide?'),
      answer: t(
        'A unified entry point for model access, API keys, usage, and billing. Connect supported text, image, and video models without maintaining separate integrations for every provider.'
      ),
    },
    {
      question: t('Which models can I use?'),
      answer: t(
        'Visit the model catalog for currently available models, supported endpoints, and group access. Model availability depends on your account and the platform configuration.'
      ),
    },
    {
      question: t('How is model usage billed?'),
      answer: t(
        'Pricing depends on the model and billing method. Review the model catalog before calling an API, and check your usage records and wallet for actual charges.'
      ),
    },
    {
      question: t('How do I migrate an existing application?'),
      answer: t(
        'Create an API key, configure the endpoint from the documentation, and select an available model. Check protocol compatibility and supported parameters before migrating your workflow.'
      ),
    },
    {
      question: t('How should I protect my API key?'),
      answer: t(
        'Keep API keys on your server, never in public repositories or browser code. Set appropriate permissions and quotas, and revoke a key immediately if it is exposed.'
      ),
    },
  ]
  return (
    <section
      className='gateway-faq gateway-section'
      aria-labelledby='gateway-faq-title'
    >
      <div className='gateway-container gateway-faq-layout'>
        <div>
          <h2 id='gateway-faq-title'>{t('Frequently asked questions')}</h2>
          <p className='gateway-faq-intro'>
            {t('A few answers before your first request.')}
          </p>
          <Button
            variant='ghost'
            className='gateway-catalog-link'
            render={<Link to='/docs' />}
          >
            {t('Docs')}
            <ArrowUpRight size={16} aria-hidden='true' />
          </Button>
        </div>
        <Accordion defaultValue={['0']}>
          {questions.map((item, index) => (
            <AccordionItem key={item.question} value={String(index)}>
              <AccordionTrigger>
                <span className='gateway-faq-number' aria-hidden='true'>
                  0{index + 1}
                </span>
                {item.question}
              </AccordionTrigger>
              <AccordionContent>
                <p>{item.answer}</p>
              </AccordionContent>
            </AccordionItem>
          ))}
        </Accordion>
      </div>
    </section>
  )
}
