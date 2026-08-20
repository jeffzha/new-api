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
  ArrowRight01Icon,
  BotIcon,
  Search01Icon,
  Store01Icon,
} from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useInfiniteQuery, useMutation, useQuery } from '@tanstack/react-query'
import { useDeferredValue, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'
import { Main } from '@/components/layout'
import { Avatar, AvatarFallback, AvatarImage } from '@/components/ui/avatar'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from '@/components/ui/empty'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { WorkbenchEntry } from '@/features/workbench-entry'
import { requireLocalSSORedirect } from '@/features/workbench-entry/selection-redirect'

import { agentStoreApi } from './api'
import { requestAgentStoreSessionTicket } from './session'
import { AgentStoreApiError, type AgentStoreItem } from './types'

export function AgentStore() {
  const { t } = useTranslation()
  const [search, setSearch] = useState('')
  const [category, setCategory] = useState('all')
  const [selected, setSelected] = useState<AgentStoreItem | null>(null)
  const deferredSearch = useDeferredValue(search.trim())
  const catalog = useInfiniteQuery({
    queryKey: ['agent-store-catalog', category, deferredSearch],
    queryFn: ({ pageParam, signal }) =>
      agentStoreApi.list(
        {
          cursor: pageParam,
          category,
          query: deferredSearch,
        },
        signal
      ),
    initialPageParam: '',
    getNextPageParam: (lastPage) => lastPage.next_cursor,
    retry: false,
    staleTime: 30_000,
  })
  const catalogItems = useMemo(
    () => catalog.data?.pages.flatMap((page) => page.items) ?? [],
    [catalog.data]
  )
  const launch = useMutation({
    mutationFn: (slug: string) => agentStoreApi.launch(slug),
    onSuccess: (result) => {
      window.location.replace(
        requireLocalSSORedirect(result.redirect_url, window.location.origin)
      )
    },
  })
  const selectedSlug = selected?.slug
  const details = useQuery({
    queryKey: ['agent-store-detail', selectedSlug],
    queryFn: ({ signal }) => {
      if (selectedSlug == null) {
        throw new Error('Agent Store detail requires a selected application')
      }
      return agentStoreApi.detail(selectedSlug, signal)
    },
    enabled: selectedSlug != null,
    retry: false,
    staleTime: 30_000,
  })

  const categories = useMemo(
    () =>
      Array.from(
        new Set(
          catalogItems.map((item) => item.category.trim()).filter(Boolean)
        )
      ).sort((left, right) => left.localeCompare(right)),
    [catalogItems]
  )

  if (catalog.isPending) return <AgentStoreSkeleton />

  if (catalog.isError) {
    const status =
      catalog.error instanceof AgentStoreApiError
        ? catalog.error.status
        : undefined
    if (status === 401) return <AgentStoreSessionBootstrap />
    if (status === 404) return <WorkbenchEntry />
    return (
      <Main>
        <ErrorState
          description={t('The Agent Store could not be loaded.')}
          onRetry={() => void catalog.refetch()}
        />
      </Main>
    )
  }

  return (
    <Main>
      <section
        className='mx-auto flex w-full max-w-7xl flex-col gap-6 py-3 sm:py-6'
        aria-labelledby='agent-store-heading'
      >
        <header className='flex flex-col justify-between gap-4 lg:flex-row lg:items-end'>
          <div className='max-w-2xl space-y-2'>
            <div className='text-primary flex items-center gap-2 text-sm font-medium'>
              <HugeiconsIcon icon={Store01Icon} className='size-4' />
              {t('Agent Store')}
            </div>
            <h1
              id='agent-store-heading'
              className='text-3xl font-semibold tracking-tight'
            >
              {t('Choose an intelligent application')}
            </h1>
            <p className='text-muted-foreground'>
              {t(
                'Open an application published for your organization. Your conversations and workspace remain isolated from other users.'
              )}
            </p>
          </div>
          <label className='relative block w-full lg:max-w-sm'>
            <span className='sr-only'>{t('Search applications')}</span>
            <HugeiconsIcon
              icon={Search01Icon}
              className='text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2'
            />
            <Input
              className='h-9 pl-9'
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              placeholder={t('Search applications')}
              type='search'
            />
          </label>
        </header>

        {categories.length > 0 && (
          <div className='flex flex-wrap gap-2' aria-label={t('Categories')}>
            <Button
              size='sm'
              variant={category === 'all' ? 'default' : 'outline'}
              onClick={() => setCategory('all')}
            >
              {t('All applications')}
            </Button>
            {categories.map((value) => (
              <Button
                key={value}
                size='sm'
                variant={category === value ? 'default' : 'outline'}
                onClick={() => setCategory(value)}
              >
                {value}
              </Button>
            ))}
          </div>
        )}

        {catalogItems.length === 0 ? (
          <Empty className='min-h-[320px] border'>
            <EmptyHeader>
              <EmptyMedia variant='icon'>
                <HugeiconsIcon icon={BotIcon} className='size-5' />
              </EmptyMedia>
              <EmptyTitle>{t('No applications found')}</EmptyTitle>
              <EmptyDescription>
                {t('Try another search or ask your administrator for access.')}
              </EmptyDescription>
            </EmptyHeader>
          </Empty>
        ) : (
          <div className='grid gap-4 md:grid-cols-2 xl:grid-cols-3'>
            {catalogItems.map((item) => (
              <AgentCard
                key={item.id}
                item={item}
                launching={launch.isPending && launch.variables === item.slug}
                onDetails={() => setSelected(item)}
                onLaunch={() => launch.mutate(item.slug)}
              />
            ))}
          </div>
        )}

        {catalog.hasNextPage && (
          <div className='flex justify-center'>
            <Button
              variant='outline'
              disabled={catalog.isFetchingNextPage}
              onClick={() => void catalog.fetchNextPage()}
            >
              {catalog.isFetchingNextPage
                ? t('Loading more...')
                : t('Load more applications')}
            </Button>
          </div>
        )}

        {launch.isError && (
          <p className='text-destructive text-sm' role='alert'>
            {t('This application could not be opened. Please try again.')}
          </p>
        )}
      </section>

      <AgentDetailsDialog
        item={details.data ?? selected}
        open={selected != null}
        launching={launch.isPending}
        detailsError={details.isError}
        onOpenChange={(open) => {
          if (!open) setSelected(null)
        }}
        onLaunch={(item) => launch.mutate(item.slug)}
      />
    </Main>
  )
}

function AgentCard({
  item,
  launching,
  onDetails,
  onLaunch,
}: {
  item: AgentStoreItem
  launching: boolean
  onDetails: () => void
  onLaunch: () => void
}) {
  const { t } = useTranslation()
  return (
    <Card className='h-full transition-shadow hover:shadow-sm'>
      <CardHeader>
        <div className='flex items-start gap-3'>
          <AgentAvatar item={item} />
          <div className='min-w-0 flex-1 space-y-1'>
            <div className='flex flex-wrap items-center gap-2'>
              <CardTitle>{item.display_name}</CardTitle>
              {item.featured && <Badge>{t('Featured')}</Badge>}
            </div>
            <CardDescription className='line-clamp-2'>
              {item.summary}
            </CardDescription>
          </div>
        </div>
      </CardHeader>
      <CardContent className='flex flex-1 flex-wrap content-start gap-2'>
        {item.category !== '' && (
          <Badge variant='secondary'>{item.category}</Badge>
        )}
        <Badge variant='outline'>{appModeLabel(t, item.app_mode)}</Badge>
        {item.tags.slice(0, 3).map((tag) => (
          <Badge key={tag} variant='outline'>
            {tag}
          </Badge>
        ))}
      </CardContent>
      <CardFooter className='gap-2'>
        <Button className='flex-1' variant='outline' onClick={onDetails}>
          {t('View details')}
        </Button>
        <Button
          className='flex-1'
          disabled={!item.available || launching}
          onClick={onLaunch}
        >
          {launching ? t('Opening...') : t('Open application')}
          {!launching && (
            <HugeiconsIcon icon={ArrowRight01Icon} className='size-4' />
          )}
        </Button>
      </CardFooter>
    </Card>
  )
}

function AgentDetailsDialog({
  item,
  open,
  launching,
  detailsError,
  onOpenChange,
  onLaunch,
}: {
  item: AgentStoreItem | null
  open: boolean
  launching: boolean
  detailsError: boolean
  onOpenChange: (open: boolean) => void
  onLaunch: (item: AgentStoreItem) => void
}) {
  const { t } = useTranslation()
  if (item == null) return null
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className='sm:max-w-lg'>
        <DialogHeader>
          <div className='flex items-center gap-3 pr-8'>
            <AgentAvatar item={item} />
            <div className='min-w-0'>
              <DialogTitle>{item.display_name}</DialogTitle>
              <DialogDescription>{item.summary}</DialogDescription>
            </div>
          </div>
        </DialogHeader>
        <div className='space-y-4'>
          {detailsError && (
            <p className='text-destructive text-sm' role='alert'>
              {t('Application details could not be loaded.')}
            </p>
          )}
          {item.description != null && item.description !== '' && (
            <p className='text-muted-foreground whitespace-pre-wrap'>
              {item.description}
            </p>
          )}
          <div className='flex flex-wrap gap-2'>
            <Badge variant='secondary'>{item.category}</Badge>
            <Badge variant='outline'>{appModeLabel(t, item.app_mode)}</Badge>
            {item.capabilities.map((capability) => (
              <Badge key={capability} variant='outline'>
                {capability}
              </Badge>
            ))}
          </div>
        </div>
        <DialogFooter>
          <Button
            disabled={!item.available || launching}
            onClick={() => onLaunch(item)}
          >
            {launching ? t('Opening...') : t('Open application')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function AgentAvatar({ item }: { item: AgentStoreItem }) {
  const avatarURL = safeAvatarURL(item.avatar_url)
  return (
    <Avatar size='lg' className='rounded-xl'>
      {avatarURL != null && (
        <AvatarImage src={avatarURL} alt='' className='rounded-xl' />
      )}
      <AvatarFallback className='rounded-xl font-medium'>
        {item.display_name.trim().slice(0, 1).toLocaleUpperCase() || 'A'}
      </AvatarFallback>
    </Avatar>
  )
}

function safeAvatarURL(value?: string): string | undefined {
  if (value == null || value.trim() === '') return undefined
  try {
    const url = new URL(value, window.location.origin)
    if (url.protocol !== 'https:' && url.origin !== window.location.origin) {
      return undefined
    }
    return url.toString()
  } catch {
    return undefined
  }
}

function appModeLabel(t: (key: string) => string, appMode: number): string {
  switch (appMode) {
    case 1:
      return t('Agent application')
    case 2:
      return t('Multi-agent application')
    case 3:
      return t('Workflow application')
    case 4:
      return t('Claw application')
    default:
      return t('Intelligent application')
  }
}

function AgentStoreSkeleton() {
  return (
    <Main>
      <div className='mx-auto w-full max-w-7xl space-y-6 py-3 sm:py-6'>
        <div className='space-y-3'>
          <Skeleton className='h-5 w-28' />
          <Skeleton className='h-9 w-80 max-w-full' />
          <Skeleton className='h-5 w-full max-w-2xl' />
        </div>
        <div className='grid gap-4 md:grid-cols-2 xl:grid-cols-3'>
          {Array.from({ length: 6 }, (_, index) => (
            <Skeleton key={index} className='h-56 rounded-xl' />
          ))}
        </div>
      </div>
    </Main>
  )
}

function AgentStoreSessionBootstrap() {
  const { t } = useTranslation()
  const redirected = useRef(false)
  const bootstrap = useQuery({
    queryKey: ['agent-store-session-bootstrap'],
    queryFn: requestAgentStoreSessionTicket,
    retry: false,
  })

  useEffect(() => {
    if (bootstrap.data == null || redirected.current) return
    redirected.current = true
    const target = new URL('/api/workbench/entry', window.location.origin)
    target.searchParams.set('ticket', bootstrap.data)
    window.location.replace(target.toString())
  }, [bootstrap.data])

  if (bootstrap.isError) {
    return (
      <Main>
        <ErrorState
          description={t('The Agent Store session could not be started.')}
          onRetry={() => void bootstrap.refetch()}
        />
      </Main>
    )
  }
  return <AgentStoreSkeleton />
}
