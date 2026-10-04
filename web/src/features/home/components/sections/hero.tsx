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
import { ArrowRight, BookOpen, Building2, KeyRound } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { useSystemConfig } from '@/hooks/use-system-config'
import { getAgencyCenterUrl } from '@/lib/agency-center'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { HeroDataFlow } from '../hero-data-flow'

interface HeroProps {
  className?: string
  isAuthenticated?: boolean
}

export function Hero(props: HeroProps) {
  const { t } = useTranslation()
  const { systemName } = useSystemConfig()
  const user = useAuthStore((state) => state.auth.user)
  const agencyUrl =
    user?.role === ROLE.SUPER_ADMIN
      ? getAgencyCenterUrl(true)
      : '/sign-in?mode=agency'

  return (
    <section className='gateway-hero' aria-labelledby='gateway-title'>
      <HeroDataFlow />
      <div className='gateway-container gateway-hero-copy'>
        <div className='gateway-hero-content'>
          <p className='gateway-eyebrow'>
            {systemName} <span aria-hidden='true'>/</span> AI MODEL GATEWAY
          </p>
          <h1 id='gateway-title'>
            {t('Large model Token aggregation and routing platform')}
          </h1>
          <p className='gateway-hero-description'>
            {t('One API. More possibilities.')}
          </p>
          <p className='gateway-hero-detail'>
            {t(
              'From conversation and code to images and video, connect your AI applications with unified access, intelligent routing, and transparent usage.'
            )}
          </p>
          <nav
            className='gateway-hero-actions'
            aria-label={t('Platform navigation')}
          >
            <Button
              className='gateway-primary-action'
              render={
                props.isAuthenticated ? (
                  <Link to='/keys' />
                ) : (
                  <Link to='/sign-in' search={{ redirect: '/keys' }} />
                )
              }
            >
              <KeyRound className='size-4' aria-hidden='true' />
              {t('Get API key')}
              <ArrowRight className='size-4' aria-hidden='true' />
            </Button>
            <Button
              variant='outline'
              className='gateway-secondary-action'
              render={<Link to='/pricing' />}
            >
              {t('View Pricing')}
            </Button>
            <Button
              variant='ghost'
              className='gateway-text-action'
              render={
                <Link to='/docs' target='_blank' rel='noopener noreferrer' />
              }
            >
              <BookOpen className='size-4' aria-hidden='true' />
              {t('Docs')}
            </Button>
            <Button
              variant='ghost'
              className='gateway-text-action'
              render={<a href={agencyUrl} />}
            >
              <Building2 className='size-4' aria-hidden='true' />
              {t('Agency Center')}
            </Button>
          </nav>
        </div>
      </div>
    </section>
  )
}
