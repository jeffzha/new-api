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
import { Boxes, ChevronRight, Minus, Plus, Search } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { cn } from '@/lib/utils'

import type { DocGroup } from '../types'
import { MethodBadge } from './method-badge'

interface DocsSidebarProps {
  groups: DocGroup[]
  allGroupIds: string[]
  activeEndpointId: string
  searchText: string
  onSearchTextChange: (value: string) => void
  onEndpointSelect: (id: string) => void
}

export function DocsSidebar({
  groups,
  allGroupIds,
  activeEndpointId,
  searchText,
  onSearchTextChange,
  onEndpointSelect,
}: DocsSidebarProps) {
  const { t } = useTranslation()
  const [collapsed, setCollapsed] = useState<Set<string>>(() => new Set())

  const toggleGroup = (id: string) => {
    setCollapsed((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  return (
    <aside className='border-r border-slate-200 bg-white lg:sticky lg:top-32 lg:h-[calc(100svh-8rem)] dark:border-slate-800 dark:bg-slate-950'>
      <div className='border-b border-slate-200 p-3 dark:border-slate-800'>
        <label className='sr-only' htmlFor='docs-search'>
          {t('Search APIs')}
        </label>
        <div className='flex items-center gap-2'>
          <div className='relative min-w-0 flex-1'>
            <Search className='pointer-events-none absolute top-2 left-2.5 size-4 text-slate-400' />
            <Input
              id='docs-search'
              value={searchText}
              onChange={(event) => onSearchTextChange(event.target.value)}
              placeholder={t('Search APIs')}
              className='h-8 rounded-md border-slate-200 bg-slate-50 pl-8 text-sm shadow-none focus-visible:bg-white dark:border-slate-800 dark:bg-slate-900 dark:focus-visible:bg-slate-950'
            />
          </div>
          <div className='flex shrink-0 items-center gap-1'>
            <Button
              type='button'
              variant='ghost'
              size='icon-sm'
              onClick={() => {
                onSearchTextChange('')
                setCollapsed(new Set())
              }}
              aria-label={t('Expand All')}
              title={t('Expand All')}
              className='text-slate-500 hover:text-slate-900 dark:text-slate-400 dark:hover:text-slate-100'
            >
              <Plus className='size-4' aria-hidden='true' />
            </Button>
            <Button
              type='button'
              variant='ghost'
              size='icon-sm'
              onClick={() => {
                onSearchTextChange('')
                setCollapsed(new Set(allGroupIds))
              }}
              aria-label={t('Collapse All')}
              title={t('Collapse All')}
              className='text-slate-500 hover:text-slate-900 dark:text-slate-400 dark:hover:text-slate-100'
            >
              <Minus className='size-4' aria-hidden='true' />
            </Button>
          </div>
        </div>
      </div>

      <nav
        aria-label={t('API overview')}
        className='h-[24rem] overflow-y-auto px-2 py-3 lg:h-[calc(100svh-11.25rem)]'
      >
        {groups.length === 0 ? (
          <div className='px-3 py-6 text-sm text-slate-500 dark:text-slate-400'>
            {t('No matching endpoints')}
          </div>
        ) : (
          <div className='space-y-4'>
            {groups.map((group) => {
              const expanded = !collapsed.has(group.id)
              return (
                <div key={group.id} className='space-y-1'>
                  <button
                    type='button'
                    onClick={() => toggleGroup(group.id)}
                    aria-expanded={expanded}
                    className='flex w-full items-center gap-1.5 rounded-md px-2 py-1 text-left text-[11px] font-semibold tracking-wide text-slate-500 transition-colors hover:bg-slate-50 hover:text-slate-800 dark:text-slate-400 dark:hover:bg-slate-900 dark:hover:text-slate-100'
                  >
                    <ChevronRight
                      className={cn(
                        'size-3.5 transition-transform',
                        expanded && 'rotate-90'
                      )}
                    />
                    <Boxes className='size-3.5' />
                    <span className='min-w-0 flex-1 truncate'>
                      {group.title}
                    </span>
                    <span className='text-[10px] text-slate-400'>
                      {group.endpoints.length}
                    </span>
                  </button>
                  {expanded ? (
                    <div className='space-y-0.5 border-l border-slate-200 pl-2 dark:border-slate-800'>
                      {group.endpoints.map((endpoint) => {
                        const active = endpoint.id === activeEndpointId
                        return (
                          <button
                            key={endpoint.id}
                            type='button'
                            onClick={() => onEndpointSelect(endpoint.id)}
                            className={cn(
                              'grid w-full grid-cols-[auto_minmax(0,1fr)] items-start gap-2 rounded-md px-2 py-1.5 text-left text-sm transition-colors',
                              active
                                ? 'bg-sky-50 text-sky-950 ring-1 ring-sky-100 dark:bg-sky-500/10 dark:text-sky-100 dark:ring-sky-500/20'
                                : 'text-slate-600 hover:bg-slate-50 hover:text-slate-950 dark:text-slate-400 dark:hover:bg-slate-900 dark:hover:text-slate-100'
                            )}
                          >
                            <MethodBadge method={endpoint.method} compact />
                            <span className='min-w-0'>
                              <span className='block truncate font-medium'>
                                {endpoint.summary}
                              </span>
                              <code
                                className={cn(
                                  'block truncate font-mono text-[11px]',
                                  active
                                    ? 'text-sky-700 dark:text-sky-300'
                                    : 'text-slate-400'
                                )}
                              >
                                {endpoint.path}
                              </code>
                            </span>
                          </button>
                        )
                      })}
                    </div>
                  ) : null}
                </div>
              )
            })}
          </div>
        )}
      </nav>
    </aside>
  )
}
