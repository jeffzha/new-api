/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { Link } from '@tanstack/react-router'
import { ArrowUpRight } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { getLobeIcon } from '@/lib/lobe-icon'

import { ShowcaseVideo } from '../showcase-video'

export function ModelShowcase() {
  const { t } = useTranslation()
  return (
    <section
      className='gateway-showcase gateway-section'
      aria-labelledby='gateway-showcase-title'
    >
      <div className='gateway-container'>
        <div className='gateway-section-heading'>
          <h2 id='gateway-showcase-title'>
            {t('Explore multimodal possibilities')}
          </h2>
        </div>
        <div className='gateway-showcase-grid'>
          <article className='gateway-media-card'>
            <ShowcaseVideo
              src='/images/home/underwater.mp4'
              poster='/images/home/underwater.webp'
              label={t('Underwater creative illustration')}
            />
            <div className='gateway-media-copy'>
              <span>{t('Text to video')}</span>
              <h3>{t('Turn imagination into motion')}</h3>
            </div>
          </article>
          <article className='gateway-media-card'>
            <ShowcaseVideo
              src='/images/home/coast.mp4'
              poster='/images/home/coast.webp'
              label={t('Coastal landscape creative illustration')}
            />
            <div className='gateway-media-copy'>
              <span>{t('Image to video')}</span>
              <h3>{t('Bring your story to life')}</h3>
            </div>
          </article>
        </div>
        <div className='gateway-showcase-footer'>
          <Button
            variant='outline'
            className='gateway-catalog-link'
            render={<Link to='/pricing' />}
          >
            {t('View all models')} <ArrowUpRight size={17} aria-hidden='true' />
          </Button>
          <p className='gateway-showcase-note'>
            {t(
              'Reference previews. Model availability and pricing are listed in the model catalog.'
            )}
          </p>
        </div>
        <div
          className='gateway-provider-strip'
          aria-label={t('Model ecosystem')}
        >
          {[
            ['GLMV.Color', 'GLM'],
            ['Kimi.Color', 'Kimi'],
            ['DeepSeek.Color', 'DeepSeek'],
            ['Hunyuan.Color', t('Tencent Hunyuan')],
            ['Qwen.Color', 'Qwen'],
            ['ByteDance.Color', 'ByteDance'],
          ].map(([icon, name]) => (
            <span key={name}>
              <span aria-hidden='true'>{getLobeIcon(icon, 27)}</span>
              {name}
            </span>
          ))}
        </div>
      </div>
    </section>
  )
}
