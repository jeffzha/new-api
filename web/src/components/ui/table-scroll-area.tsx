/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { useLayoutEffect, useRef, type ReactNode } from 'react'

import { cn } from '@/lib/utils'

import { ScrollArea } from './scroll-area'

export function TableScrollArea(props: {
  children: ReactNode
  className?: string
  viewportClassName?: string
}) {
  const root = useRef<HTMLDivElement>(null)
  const content = useRef<HTMLDivElement>(null)

  useLayoutEffect(() => {
    const header = content.current?.querySelector('thead')
    if (!header) return

    // The scrollbar starts at the real header edge, including wrapped/grouped headers.
    const updateHeight = (height: number) => {
      root.current?.style.setProperty('--table-header-height', `${height}px`)
    }
    updateHeight(header.offsetHeight)
    const observer = new ResizeObserver(([entry]) => {
      updateHeight(entry?.borderBoxSize[0]?.blockSize ?? header.offsetHeight)
    })
    observer.observe(header, { box: 'border-box' })
    return () => observer.disconnect()
  }, [props.children])

  return (
    <ScrollArea
      ref={root}
      data-slot='table-container'
      className={cn('table-scroll-area', props.className)}
      viewportProps={{
        className: cn('table-scroll-viewport', props.viewportClassName),
      }}
      contentProps={{ ref: content, className: 'min-w-full' }}
      verticalScrollbarProps={{
        keepMounted: true,
        style: { top: 'var(--table-header-height)', height: 'auto' },
      }}
      horizontal
    >
      {props.children}
    </ScrollArea>
  )
}
