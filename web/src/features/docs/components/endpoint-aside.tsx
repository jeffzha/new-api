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
import { Check, Code2, Copy, Link2, Server } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { useCopyToClipboard } from '@/hooks/use-copy-to-clipboard'
import { cn } from '@/lib/utils'

import type { DocEndpoint, DocExample } from '../types'
import { MethodBadge } from './method-badge'

interface CodePanelProps {
  title: string
  examples: DocExample[]
  emptyText: string
}

function CodePanel({ title, examples, emptyText }: CodePanelProps) {
  const { t } = useTranslation()
  const { copiedText, copyToClipboard } = useCopyToClipboard({ notify: true })
  const [activeId, setActiveId] = useState(examples[0]?.id ?? '')
  const active =
    examples.find((example) => example.id === activeId) ?? examples[0]
  const copied = !!active && copiedText === active.code

  return (
    <section className='overflow-hidden rounded-lg border border-slate-800 bg-slate-950 shadow-[0_12px_30px_rgba(15,23,42,0.18)]'>
      <div className='flex items-center justify-between border-b border-slate-800 px-3 py-2'>
        <div className='flex min-w-0 items-center gap-2'>
          <Code2 className='size-4 text-sky-300' />
          <h2 className='truncate text-sm font-semibold text-slate-100'>
            {title}
          </h2>
        </div>
        {active ? (
          <Button
            variant='ghost'
            size='icon-sm'
            className='text-slate-300 hover:bg-slate-800 hover:text-white'
            onClick={() => copyToClipboard(active.code)}
            aria-label={t('Copy example')}
            title={t('Copy example')}
          >
            {copied ? (
              <Check className='size-4 text-emerald-300' />
            ) : (
              <Copy className='size-4' />
            )}
          </Button>
        ) : null}
      </div>
      {active ? (
        <>
          <div className='flex min-w-0 gap-1 overflow-x-auto border-b border-slate-800 px-2 py-1.5'>
            {examples.map((example) => (
              <button
                key={example.id}
                type='button'
                onClick={() => setActiveId(example.id)}
                className={cn(
                  'h-6 shrink-0 rounded-md px-2 text-xs font-medium transition-colors',
                  example.id === active.id
                    ? 'bg-slate-800 text-white'
                    : 'text-slate-400 hover:bg-slate-900 hover:text-slate-100'
                )}
              >
                {example.label}
              </button>
            ))}
          </div>
          <pre className='max-h-[26rem] overflow-auto p-3 text-xs leading-6 text-slate-100'>
            <code>{active.code}</code>
          </pre>
        </>
      ) : (
        <div className='px-3 py-8 text-sm text-slate-400'>{emptyText}</div>
      )}
    </section>
  )
}

/** Colour a response status chip green for 2xx, red for 4xx/5xx, grey otherwise. */
function statusToneClass(code: string): string {
  if (code.startsWith('2')) {
    return 'bg-emerald-50 text-emerald-700 ring-emerald-100 dark:bg-emerald-500/10 dark:text-emerald-300 dark:ring-emerald-500/20'
  }
  if (code.startsWith('4') || code.startsWith('5')) {
    return 'bg-rose-50 text-rose-700 ring-rose-100 dark:bg-rose-500/10 dark:text-rose-300 dark:ring-rose-500/20'
  }
  return 'bg-slate-100 text-slate-600 ring-slate-200 dark:bg-slate-800 dark:text-slate-300 dark:ring-slate-700'
}

function ResponsesPanel({ statusCodes }: { statusCodes: string[] }) {
  const { t } = useTranslation()
  return (
    <section className='rounded-lg border border-slate-200 bg-white p-3 text-sm leading-6 dark:border-slate-800 dark:bg-slate-950'>
      <div className='mb-2 flex items-center gap-2 font-semibold text-slate-900 dark:text-slate-100'>
        <Server className='size-4 text-slate-400' />
        {t('Responses')}
      </div>
      <div className='flex flex-wrap gap-1.5'>
        {statusCodes.map((code) => (
          <span
            key={code}
            className={cn(
              'rounded-md px-2 py-0.5 font-mono text-xs ring-1',
              statusToneClass(code)
            )}
          >
            {code}
          </span>
        ))}
      </div>
    </section>
  )
}

export function EndpointAside({ endpoint }: { endpoint: DocEndpoint }) {
  const { t } = useTranslation()
  const requestExamples = [endpoint.curlExample, ...endpoint.requestExamples]

  return (
    <aside className='border-l border-slate-200 bg-slate-50 xl:sticky xl:top-32 xl:h-[calc(100svh-8rem)] dark:border-slate-800 dark:bg-slate-900/60'>
      <div className='space-y-4 overflow-y-auto px-4 py-4 xl:h-full'>
        <section className='space-y-2'>
          <div className='flex items-center gap-2 text-sm font-semibold text-slate-900 dark:text-slate-100'>
            <Link2 className='size-4 text-slate-400' />
            {t('Endpoint')}
          </div>
          <div className='inline-flex max-w-full items-center gap-2 rounded-md border border-slate-200 bg-white px-2 py-1.5 dark:border-slate-800 dark:bg-slate-950'>
            <MethodBadge method={endpoint.method} />
            <code className='truncate font-mono text-xs text-slate-700 dark:text-slate-200'>
              {endpoint.path}
            </code>
          </div>
        </section>
        <CodePanel
          title={t('Request example')}
          examples={requestExamples}
          emptyText={t('No request example in this section.')}
        />
        <CodePanel
          title={t('Response example')}
          examples={endpoint.responseExamples}
          emptyText={t('No response example in this section.')}
        />
        <ResponsesPanel statusCodes={endpoint.responseStatusCodes} />
      </div>
    </aside>
  )
}
