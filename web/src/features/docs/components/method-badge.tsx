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
import { cn } from '@/lib/utils'

/** Per-method colour palette for the little uppercase method chips. */
const METHOD_BADGE_STYLES: Record<string, string> = {
  GET: 'border-emerald-200 bg-emerald-50 text-emerald-700 dark:border-emerald-500/30 dark:bg-emerald-500/10 dark:text-emerald-300',
  POST: 'border-sky-200 bg-sky-50 text-sky-700 dark:border-sky-500/30 dark:bg-sky-500/10 dark:text-sky-300',
  PUT: 'border-amber-200 bg-amber-50 text-amber-700 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-300',
  PATCH:
    'border-violet-200 bg-violet-50 text-violet-700 dark:border-violet-500/30 dark:bg-violet-500/10 dark:text-violet-300',
  DELETE:
    'border-rose-200 bg-rose-50 text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300',
  HEAD: 'border-stone-200 bg-stone-50 text-stone-700 dark:border-stone-500/30 dark:bg-stone-500/10 dark:text-stone-300',
  OPTIONS:
    'border-teal-200 bg-teal-50 text-teal-700 dark:border-teal-500/30 dark:bg-teal-500/10 dark:text-teal-300',
}

interface MethodBadgeProps {
  method: string
  compact?: boolean
}

export function MethodBadge({ method, compact }: MethodBadgeProps) {
  return (
    <span
      className={cn(
        'inline-flex shrink-0 items-center rounded border font-mono font-semibold',
        compact ? 'h-4 px-1 text-[9px]' : 'h-5 px-1.5 text-[10px]',
        METHOD_BADGE_STYLES[method] || METHOD_BADGE_STYLES.POST
      )}
    >
      {method}
    </span>
  )
}
