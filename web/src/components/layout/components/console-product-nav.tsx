/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { Link, useLocation } from '@tanstack/react-router'
import {
  ArrowUpRight,
  BookOpen,
  Boxes,
  House,
  Info,
  LayoutDashboard,
  Trophy,
} from 'lucide-react'
import type { MouseEvent } from 'react'
import { useTranslation } from 'react-i18next'

import type { TopNavLink } from '../types'

const navigationIcons = {
  '/': House,
  '/dashboard': LayoutDashboard,
  '/pricing': Boxes,
  '/rankings': Trophy,
  '/about': Info,
}

export function ConsoleProductNav(props: {
  links: TopNavLink[]
  console?: boolean
  onNavigate?: (event: MouseEvent<HTMLAnchorElement>, link: TopNavLink) => void
}) {
  const { t } = useTranslation()
  const pathname = useLocation({ select: (l) => l.pathname })
  const active = props.links.find((link) => {
    if (link.external || link.disabled) return false
    if (props.console) return link.href === '/dashboard'
    return (
      pathname === link.href ||
      (link.href !== '/' && pathname.startsWith(`${link.href}/`))
    )
  })
  const onNavigate = (
    event: MouseEvent<HTMLAnchorElement>,
    link: TopNavLink
  ) => {
    if (link.disabled) event.preventDefault()
    else props.onNavigate?.(event, link)
  }
  return (
    <nav className='console-product-nav' aria-label={t('Platform navigation')}>
      {props.links.map((link) => {
        const Icon =
          navigationIcons[link.href as keyof typeof navigationIcons] ?? BookOpen
        if (link.disabled) {
          return (
            <span
              key={link.href}
              className='console-product-link'
              aria-disabled='true'
            >
              <Icon aria-hidden />
              {t(link.title)}
            </span>
          )
        }
        if (link.external) {
          return (
            <a
              key={link.href}
              href={link.href}
              target='_blank'
              rel='noopener noreferrer'
              className='console-product-link'
              onClick={(event) => onNavigate(event, link)}
            >
              <Icon aria-hidden />
              {t(link.title)}
              <ArrowUpRight aria-hidden />
            </a>
          )
        }
        return (
          <Link
            key={link.href}
            to={link.href}
            activeOptions={{ exact: link.href === '/' }}
            aria-current={active?.href === link.href ? 'page' : undefined}
            className='console-product-link'
            onClick={(event) => onNavigate(event, link)}
          >
            <Icon aria-hidden />
            {t(link.title)}
          </Link>
        )
      })}
    </nav>
  )
}
