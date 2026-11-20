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

import { StatusBadge } from '@/components/status-badge'
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from '@/components/ui/tooltip'

import type { UsageLog } from '../data/schema'
import { LogCostBadge } from './log-cost-display'

export function OpsFeeCell(props: { fee: UsageLog['ops_fee'] }) {
  const { t } = useTranslation()
  const fee = props.fee
  if (!fee) {
    return (
      <TooltipProvider>
        <Tooltip>
          <TooltipTrigger
            render={
              <span
                tabIndex={0}
                className='text-muted-foreground'
                aria-label={t('No operations fee for this call')}
              >
                —
              </span>
            }
          />
          <TooltipContent>
            {t('No operations fee for this call')}
          </TooltipContent>
        </Tooltip>
      </TooltipProvider>
    )
  }
  if (fee.status !== 'available' || fee.amount === null) {
    const label =
      fee.status === 'waiting'
        ? t('Awaiting operations fee')
        : t('Operations fee unconfirmed')
    return <StatusBadge label={label} variant='neutral' copyable={false} />
  }
  return <LogCostBadge value={`¥${fee.amount}`} />
}
