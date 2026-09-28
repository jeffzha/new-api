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
import { useTranslation } from 'react-i18next'

import { Main } from '@/components/layout'
import {
  CardStaggerContainer,
  CardStaggerItem,
} from '@/components/page-transition'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useThemeCustomization } from '@/context/theme-customization-provider'
import { useStatus } from '@/hooks/use-status'
import { usesConsoleWorkspace } from '@/lib/theme-customization'
import { useAuthStore } from '@/stores/auth-store'

import { CheckinCalendarCard } from './components/checkin-calendar-card'
import { LanguagePreferencesCard } from './components/language-preferences-card'
import { ProfileHeader } from './components/profile-header'
import { ProfileSettingsCard } from './components/profile-settings-card'
import { SidebarModulesCard } from './components/sidebar-modules-card'
import { useProfile } from './hooks'

export function Profile() {
  const { t } = useTranslation()
  const { customization } = useThemeCustomization()
  const signal = usesConsoleWorkspace(customization.preset)
  const { profile, loading, refreshProfile } = useProfile()
  const { status } = useStatus()
  const permissions = useAuthStore((s) => s.auth.user?.permissions)

  const checkinEnabled = status?.checkin_enabled === true
  const turnstileEnabled = !!(
    status?.turnstile_check && status?.turnstile_site_key
  )
  const turnstileSiteKey = status?.turnstile_site_key || ''
  const canConfigureSidebar = permissions?.sidebar_settings !== false

  return (
    <Main data-signal-page='profile'>
      <div className='signal-profile-content min-h-0 flex-1 overflow-auto px-3 py-3 sm:px-4 sm:py-6'>
        <CardStaggerContainer className='signal-profile-layout mx-auto flex w-full max-w-7xl flex-col gap-4 sm:gap-6'>
          <CardStaggerItem>
            <ProfileHeader profile={profile} loading={loading} />
          </CardStaggerItem>

          <CardStaggerItem>
            <div className='signal-profile-grid grid gap-4 sm:gap-5 xl:grid-cols-[minmax(0,1fr)_minmax(360px,0.46fr)] xl:items-start'>
              {signal ? (
                <Tabs
                  defaultValue='account'
                  className='signal-settings-tabs col-span-full min-w-0'
                >
                  <TabsList variant='line' aria-label={t('Profile')}>
                    <TabsTrigger value='account'>
                      {t('Account settings')}
                    </TabsTrigger>
                    <TabsTrigger value='preferences'>
                      {t('Preferences')}
                    </TabsTrigger>
                    {checkinEnabled && (
                      <TabsTrigger value='checkin'>{t('Check-in')}</TabsTrigger>
                    )}
                  </TabsList>
                  <TabsContent
                    value='account'
                    keepMounted
                    className='space-y-6'
                  >
                    {profile && (
                      <section
                        aria-label={t('Account Information')}
                        className='border-b pb-6'
                      >
                        <h3 className='mb-4 text-base font-semibold'>
                          {t('Account Information')}
                        </h3>
                        <dl className='grid min-w-0 gap-x-8 gap-y-5 sm:grid-cols-2 xl:grid-cols-4'>
                          {[
                            { label: t('Username'), value: profile.username },
                            {
                              label: t('Display Name'),
                              value: profile.display_name || profile.username,
                            },
                            {
                              label: t('Email'),
                              value: profile.email || t('Not bound'),
                            },
                            { label: t('Group'), value: profile.group },
                          ].map((item) => (
                            <div key={item.label} className='min-w-0'>
                              <dt className='text-muted-foreground text-xs'>
                                {item.label}
                              </dt>
                              <dd className='mt-2 text-sm font-medium break-words'>
                                {item.value}
                              </dd>
                            </div>
                          ))}
                        </dl>
                      </section>
                    )}
                    <ProfileSettingsCard
                      profile={profile}
                      loading={loading}
                      onProfileUpdate={refreshProfile}
                    />
                  </TabsContent>
                  <TabsContent
                    value='preferences'
                    keepMounted
                    className='space-y-6'
                  >
                    <LanguagePreferencesCard
                      profile={profile}
                      onProfileUpdate={refreshProfile}
                    />
                    {canConfigureSidebar && <SidebarModulesCard />}
                  </TabsContent>
                  {checkinEnabled && (
                    <TabsContent value='checkin' keepMounted>
                      <CheckinCalendarCard
                        checkinEnabled={checkinEnabled}
                        turnstileEnabled={turnstileEnabled}
                        turnstileSiteKey={turnstileSiteKey}
                      />
                    </TabsContent>
                  )}
                </Tabs>
              ) : (
                <>
                  <div className='space-y-4 sm:space-y-6'>
                    <ProfileSettingsCard
                      profile={profile}
                      loading={loading}
                      onProfileUpdate={refreshProfile}
                    />
                    <LanguagePreferencesCard
                      profile={profile}
                      onProfileUpdate={refreshProfile}
                    />
                  </div>

                  <div className='space-y-4 sm:space-y-6 xl:sticky xl:top-6'>
                    {checkinEnabled && (
                      <CheckinCalendarCard
                        checkinEnabled={checkinEnabled}
                        turnstileEnabled={turnstileEnabled}
                        turnstileSiteKey={turnstileSiteKey}
                      />
                    )}
                    {canConfigureSidebar && <SidebarModulesCard />}
                  </div>
                </>
              )}
            </div>
          </CardStaggerItem>
        </CardStaggerContainer>
      </div>
    </Main>
  )
}
