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
import { ChevronRight, ShieldCheck } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import type { DocModel } from '../types'

interface DocsHeaderProps {
  doc: DocModel
}

export function DocsHeader({ doc }: DocsHeaderProps) {
  const { t } = useTranslation()
  const source = doc.source

  return (
    <header className='sticky top-16 z-30 border-b border-slate-200 bg-white/95 backdrop-blur dark:border-slate-800 dark:bg-slate-950/95'>
      <div className='flex min-h-16 flex-col gap-3 px-4 py-3 lg:flex-row lg:items-center lg:justify-between lg:px-6'>
        <div className='min-w-0 space-y-1'>
          <div className='flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-sm'>
            <span className='font-semibold text-slate-950 dark:text-slate-50'>
              Nexus Reach
            </span>
            <ChevronRight className='size-3.5 text-slate-400' />
            <span className='text-slate-500 dark:text-slate-400'>
              {t('API docs')}
            </span>
            <ChevronRight className='size-3.5 text-slate-400' />
            <span className='font-medium text-slate-900 dark:text-slate-100'>
              {t(source.title)}
            </span>
          </div>
          <div className='flex min-w-0 flex-wrap items-center gap-2 text-xs text-slate-500 dark:text-slate-400'>
            <span className='inline-flex h-6 items-center gap-1.5 rounded-md border border-emerald-200 bg-emerald-50 px-2 font-medium text-emerald-700 dark:border-emerald-500/30 dark:bg-emerald-500/10 dark:text-emerald-300'>
              <ShieldCheck className='size-3' />
              {`OpenAPI ${source.spec.openapi}`}
            </span>
            <span className='inline-flex h-6 items-center rounded-md bg-slate-100 px-2 dark:bg-slate-800'>
              {source.spec.info.version}
            </span>
            <span className='inline-flex h-6 items-center rounded-md bg-slate-100 px-2 dark:bg-slate-800'>
              {t('{{count}} endpoints', { count: doc.endpointCount })}
            </span>
            <span className='inline-flex h-6 items-center rounded-md bg-slate-100 px-2 dark:bg-slate-800'>
              {t('{{count}} tags', { count: doc.groups.length })}
            </span>
          </div>
        </div>
      </div>
    </header>
  )
}
