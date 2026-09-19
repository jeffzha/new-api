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

interface StatsProps {
  className?: string
}

interface StatItem {
  value: string
  label: string
}

export function Stats(_props: StatsProps) {
  const { t } = useTranslation()
  const stats: StatItem[] = [
    { value: t('1.2 trillion'), label: t('Daily token calls') },
    { value: t('2 billion TPM'), label: t('Platform peak throughput') },
    { value: '200+', label: t('Global mainstream models') },
    { value: '99.9%+', label: t('Platform availability SLA') },
  ]

  return (
    <div className='relative z-10 mt-12 md:mt-14'>
      <div className='grid grid-cols-2 gap-3 md:grid-cols-4'>
        {stats.map((s) => (
          <div
            key={s.label}
            className='border-primary/10 bg-primary/[0.055] dark:bg-primary/[0.08] flex min-h-24 flex-col items-center justify-center rounded-lg border px-3 py-5 text-center'
          >
            <span className='text-primary text-2xl font-bold tracking-normal md:text-3xl'>
              {s.value}
            </span>
            <span className='text-muted-foreground mt-1.5 text-xs'>
              {s.label}
            </span>
          </div>
        ))}
      </div>
    </div>
  )
}
