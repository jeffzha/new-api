import { useQuery } from '@tanstack/react-query'
import { Activity, Bell, History, SlidersHorizontal } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'
import { SectionPageLayout } from '@/components/layout'
import { LoadingState } from '@/components/loading-state'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'

import { loadHealth, type Access, type Selection } from './api'
import { HealthOverview } from './overview'
import { HealthRecords } from './records'
import { HealthNotifications, HealthRules } from './settings'

export function ModelHealth() {
  const { t } = useTranslation()
  const [tab, setTab] = useState('overview')
  const [selection, setSelection] = useState<Selection>()
  const access = useQuery({
    queryKey: ['model-health', 'access'],
    queryFn: ({ signal }) =>
      loadHealth<Access>('/capabilities', undefined, signal),
  })
  function openRules(target: Selection) {
    setSelection(target)
    setTab('rules')
  }

  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>{t('Model Health')}</SectionPageLayout.Title>
      <SectionPageLayout.Content>
        {access.isPending && <LoadingState />}
        {access.isError && <ErrorState onRetry={() => void access.refetch()} />}
        {access.data && (
          <Tabs value={tab} onValueChange={setTab}>
            <div className='overflow-x-auto border-b pb-1'>
              <TabsList variant='line' aria-label={t('Model Health')}>
                <TabsTrigger value='overview' icon={<Activity />}>
                  {t('Health overview')}
                </TabsTrigger>
                <TabsTrigger value='records' icon={<History />}>
                  {t('Detection records')}
                </TabsTrigger>
                {access.data.rules && (
                  <TabsTrigger value='rules' icon={<SlidersHorizontal />}>
                    {t('Detection rules')}
                  </TabsTrigger>
                )}
                {access.data.notify && (
                  <TabsTrigger value='notify' icon={<Bell />}>
                    {t('Notification settings')}
                  </TabsTrigger>
                )}
              </TabsList>
            </div>
            <TabsContent value='overview'>
              <HealthOverview access={access.data} onRules={openRules} />
            </TabsContent>
            <TabsContent value='records'>
              <HealthRecords access={access.data} />
            </TabsContent>
            {access.data.rules && (
              <TabsContent value='rules'>
                <HealthRules selection={selection} />
              </TabsContent>
            )}
            {access.data.notify && (
              <TabsContent value='notify'>
                <HealthNotifications worker={access.data.worker} />
              </TabsContent>
            )}
          </Tabs>
        )}
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
