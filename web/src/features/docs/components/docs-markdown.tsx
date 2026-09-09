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
import { Marked } from 'marked'
import { useMemo } from 'react'

import { cn } from '@/lib/utils'

/**
 * Lightweight markdown renderer for endpoint descriptions.
 *
 * The shared `@/components/ui/markdown` pulls in KaTeX + fonts (~hundreds of KB)
 * for math the docs never use. Descriptions are bundled, trusted spec content
 * (paragraphs, lists, inline code, bold) so plain `marked` output is enough.
 */
const marked = new Marked({ gfm: true, breaks: false })

interface DocsMarkdownProps {
  children: string
  className?: string
}

export function DocsMarkdown({ children, className }: DocsMarkdownProps) {
  const html = useMemo(
    () => marked.parse(children, { async: false }) as string,
    [children]
  )
  return (
    <div
      className={cn(
        'text-sm leading-7 text-slate-600 dark:text-slate-300',
        '[&_p]:my-2 [&_strong]:font-semibold [&_em]:italic',
        '[&_a]:text-sky-600 [&_a]:underline dark:[&_a]:text-sky-400',
        '[&_ul]:my-2 [&_ul]:list-disc [&_ul]:pl-5 [&_ol]:my-2 [&_ol]:list-decimal [&_ol]:pl-5 [&_li]:my-1',
        '[&_code]:rounded [&_code]:bg-slate-100 [&_code]:px-1 [&_code]:py-0.5 [&_code]:font-mono [&_code]:text-[13px] [&_code]:text-slate-800 dark:[&_code]:bg-slate-800 dark:[&_code]:text-slate-100',
        '[&_pre]:my-3 [&_pre]:overflow-x-auto [&_pre]:rounded-md [&_pre]:bg-slate-900 [&_pre]:p-3 [&_pre_code]:bg-transparent [&_pre_code]:p-0 [&_pre_code]:text-slate-100',
        className
      )}
      // Content is bundled, trusted OpenAPI spec text (not user input).
      // eslint-disable-next-line react/no-danger
      dangerouslySetInnerHTML={{ __html: html }}
    />
  )
}
