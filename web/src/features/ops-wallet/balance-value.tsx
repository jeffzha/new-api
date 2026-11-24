import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from '@/components/ui/tooltip'

import type { OPSBalance } from './types'
import { useOPSBalance } from './use-ops-balance'

export function OPSBalanceValue(props: {
  balance?: OPSBalance
  failed?: boolean
}) {
  const balance = useOPSBalance(props.balance, props.failed)
  return (
    <TooltipProvider>
      <Tooltip>
        <TooltipTrigger
          render={
            <span
              tabIndex={0}
              aria-label={balance.hint}
              className='tabular-nums'
            >
              {balance.text}
            </span>
          }
        />
        <TooltipContent>{balance.hint}</TooltipContent>
      </Tooltip>
    </TooltipProvider>
  )
}
