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
    <SectionPageLayout fixedContent>
      <SectionPageLayout.Title>{t('Model Health')}</SectionPageLayout.Title>
      <SectionPageLayout.Content>
        {access.isPending && <LoadingState />}
        {access.isError && <ErrorState onRetry={() => void access.refetch()} />}
        {access.data && (
          <Tabs
            value={tab}
            onValueChange={setTab}
            className='h-full min-h-0 gap-0 [--background:var(--color-white)] [--border:var(--color-slate-200)] [--card:var(--color-white)] [--muted:var(--color-slate-50)] dark:[--background:var(--color-slate-950)] dark:[--border:var(--color-slate-700)] dark:[--card:var(--color-slate-900)] dark:[--muted:var(--color-slate-800)]'
          >
            <div className='shrink-0 overflow-x-auto border-b pb-2'>
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
            <TabsContent value='overview' className='min-h-0 overflow-hidden'>
              <HealthOverview access={access.data} onRules={openRules} />
            </TabsContent>
            <TabsContent value='records' className='min-h-0 overflow-hidden'>
              <HealthRecords access={access.data} />
            </TabsContent>
            {access.data.rules && (
              <TabsContent value='rules' className='min-h-0 overflow-y-auto'>
                <HealthRules selection={selection} />
              </TabsContent>
            )}
            {access.data.notify && (
              <TabsContent value='notify' className='min-h-0 overflow-y-auto'>
                <HealthNotifications worker={access.data.worker} />
              </TabsContent>
            )}
          </Tabs>
        )}
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
