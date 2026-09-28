/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { useLocation } from '@tanstack/react-router'
import { ChevronDown, X } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { SidebarContent, useSidebar } from '@/components/ui/sidebar'

import { checkIsActive } from '../lib/url-utils'
import type { NavGroup as NavGroupType } from '../types'
import { NavGroup } from './nav-group'

export function ConsoleSidebar(props: { groups: NavGroupType[] }) {
  const { t } = useTranslation()
  const { isMobile, setOpenMobile } = useSidebar()

  return (
    <>
      {isMobile && (
        <div className='console-mobile-heading'>
          <span>{t('Console')}</span>
          <Button
            variant='ghost'
            size='icon'
            aria-label={t('Close navigation')}
            onClick={() => setOpenMobile(false)}
          >
            <X aria-hidden />
          </Button>
        </div>
      )}
      <SidebarContent className='console-navigator'>
        {props.groups
          .filter((group) => group.items.length > 0)
          .map((group) => (
            <ConsoleNavGroup key={group.id || group.title} group={group} />
          ))}
      </SidebarContent>
    </>
  )
}

function ConsoleNavGroup(props: { group: NavGroupType }) {
  const href = useLocation({ select: (location) => location.href })
  const { state, isMobile } = useSidebar()
  const collapsed = state === 'collapsed' && !isMobile
  const active = props.group.items.some(
    (item) =>
      checkIsActive(href, item) ||
      (item.type === 'chat-presets' && href.startsWith('/chat/'))
  )
  const [selection, setSelection] = useState({ href, open: true })
  // Reopen the current group after route navigation, but allow manual collapse.
  const open =
    collapsed || (active && selection.href !== href) || selection.open

  return (
    <nav aria-label={props.group.title} className='console-nav-group'>
      <Collapsible
        open={open}
        onOpenChange={(next) => setSelection({ href, open: next })}
      >
        {!collapsed && (
          <CollapsibleTrigger
            className='console-group-trigger'
            render={<Button variant='ghost' />}
          >
            <span>{props.group.title}</span>
            <ChevronDown aria-hidden />
          </CollapsibleTrigger>
        )}
        <CollapsibleContent>
          <NavGroup {...props.group} hideLabel />
        </CollapsibleContent>
      </Collapsible>
    </nav>
  )
}
