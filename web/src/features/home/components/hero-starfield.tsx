/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { Pause, Play } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'

export function HeroStarfield() {
  const { t } = useTranslation()
  const [paused, setPaused] = useState(false)
  const action = paused ? t('Play preview') : t('Pause preview')
  return (
    <>
      <div
        className='gateway-starfield'
        aria-hidden='true'
        data-paused={paused}
      >
        {['left', 'right'].map((side) => (
          <div
            key={side}
            className={`gateway-starfield-side gateway-starfield-${side}`}
          >
            {Array.from({ length: 16 }, (_, index) => (
              <span key={index} className='gateway-star' />
            ))}
          </div>
        ))}
      </div>
      <Tooltip>
        <TooltipTrigger
          render={
            <Button
              variant='ghost'
              size='icon'
              className='gateway-starfield-toggle'
              aria-label={action}
              aria-pressed={paused}
              onClick={() => setPaused((value) => !value)}
            >
              {paused ? (
                <Play aria-hidden='true' />
              ) : (
                <Pause aria-hidden='true' />
              )}
            </Button>
          }
        />
        <TooltipContent>{action}</TooltipContent>
      </Tooltip>
    </>
  )
}
