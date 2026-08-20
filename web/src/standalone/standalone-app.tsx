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
import { Moon, Sun } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Toaster } from '@/components/ui/sonner'
import { ThemeProvider, useTheme } from '@/context/theme-provider'
import { DocsContent } from '@/features/docs/docs-content'

function StandaloneShell() {
  const { resolvedTheme, setTheme } = useTheme()
  return (
    <>
      <header className='fixed inset-x-0 top-0 z-40 flex h-16 items-center justify-between border-b border-slate-200 bg-white/95 px-4 backdrop-blur sm:px-6 dark:border-slate-800 dark:bg-slate-950/95'>
        <div className='flex items-center gap-2'>
          <span className='text-base font-semibold text-slate-950 dark:text-slate-50'>
            Nexus Reach
          </span>
          <span className='rounded-md bg-slate-100 px-2 py-0.5 text-xs font-medium text-slate-500 dark:bg-slate-800 dark:text-slate-400'>
            API Docs
          </span>
        </div>
        <Button
          type='button'
          variant='ghost'
          size='icon-sm'
          aria-label='Toggle theme'
          onClick={() => setTheme(resolvedTheme === 'dark' ? 'light' : 'dark')}
          className='text-slate-500 hover:text-slate-900 dark:text-slate-400 dark:hover:text-slate-100'
        >
          {resolvedTheme === 'dark' ? (
            <Sun className='size-4' />
          ) : (
            <Moon className='size-4' />
          )}
        </Button>
      </header>
      <DocsContent />
    </>
  )
}

/** Root of the standalone static docs page — provider stack + shell. */
export function StandaloneApp() {
  return (
    <ThemeProvider>
      <StandaloneShell />
      <Toaster />
    </ThemeProvider>
  )
}
