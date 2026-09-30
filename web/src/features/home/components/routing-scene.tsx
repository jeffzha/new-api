/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { Cpu, KeyRound, Pause, Play } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { getLobeIcon } from '@/lib/lobe-icon'

const providers = [
  { name: 'OpenAI', icon: 'OpenAI', color: '#15937b' },
  { name: 'Claude', icon: 'Claude.Color', color: '#bf7356' },
  { name: 'DeepSeek', icon: 'DeepSeek.Color', color: '#407be8' },
  { name: 'Gemini', icon: 'Gemini.Color', color: '#7972c8' },
]

export function RoutingScene(props: { systemName: string }) {
  const { t } = useTranslation()
  const [selected, setSelected] = useState(2)
  const [paused, setPaused] = useState(false)
  const rootRef = useRef<HTMLDivElement>(null)
  const canvasRef = useRef<HTMLCanvasElement>(null)

  useEffect(() => {
    const root = rootRef.current
    const canvas = canvasRef.current
    if (!root || !canvas) return
    const context = canvas.getContext('2d')
    if (!context) return
    const motion = window.matchMedia('(prefers-reduced-motion: reduce)')
    let frame = 0
    let visible = true
    let width = 0
    let height = 0
    let gridColor = ''
    let nodes: { left: number; right: number; y: number }[] = []

    const draw = (time: number) => {
      context.clearRect(0, 0, width, height)
      context.strokeStyle = gridColor
      context.lineWidth = 1
      // A quiet coordinate grid places the routing diagram in the same plane as the page.
      const gridStart = width > 800 ? width * 0.48 : 0
      context.globalAlpha = 0.55
      for (let x = gridStart; x < width; x += 44) {
        context.beginPath()
        context.moveTo(x, 36)
        context.lineTo(x, height)
        context.stroke()
      }
      for (let y = 36; y < height; y += 44) {
        context.beginPath()
        context.moveTo(gridStart, y)
        context.lineTo(width, y)
        context.stroke()
      }
      context.globalAlpha = 1
      if (nodes.length >= 6) {
        const [source, hub, ...destinations] = nodes
        const routes = [
          [
            { x: source.right, y: source.y },
            { x: hub.left, y: hub.y },
          ],
          ...destinations.map((destination) => [
            { x: hub.right, y: hub.y },
            { x: destination.left, y: destination.y },
          ]),
        ]
        routes.forEach(([start, end], index) => {
          const active = index === 0 || index === selected + 1
          const bend = (start.x + end.x) / 2
          context.beginPath()
          context.moveTo(start.x, start.y)
          context.bezierCurveTo(bend, start.y, bend, end.y, end.x, end.y)
          context.strokeStyle = active ? providers[selected].color : gridColor
          context.globalAlpha = active ? 0.6 : 1
          context.lineWidth = active ? 1.5 : 1
          context.stroke()
          context.globalAlpha = 1
          if (!active) return
          const progress =
            paused || motion.matches ? 0.62 : (time / 3200 + index * 0.2) % 1
          const inverse = 1 - progress
          const x =
            inverse ** 3 * start.x +
            3 * inverse ** 2 * progress * bend +
            3 * inverse * progress ** 2 * bend +
            progress ** 3 * end.x
          const y =
            inverse ** 3 * start.y +
            3 * inverse ** 2 * progress * start.y +
            3 * inverse * progress ** 2 * end.y +
            progress ** 3 * end.y
          context.fillStyle = providers[selected].color
          context.fillRect(x - 3, y - 3, 6, 6)
        })
      }
      if (!paused && !motion.matches && visible && !document.hidden) {
        frame = requestAnimationFrame(draw)
      }
    }
    const measure = () => {
      gridColor = getComputedStyle(root).getPropertyValue('--home-grid').trim()
      const bounds = root.getBoundingClientRect()
      width = bounds.width
      height = bounds.height
      const ratio = Math.min(window.devicePixelRatio || 1, 2)
      canvas.width = Math.round(width * ratio)
      canvas.height = Math.round(height * ratio)
      context.setTransform(ratio, 0, 0, ratio, 0, 0)
      nodes = [
        ...root.querySelectorAll<HTMLElement>('[data-routing-node]'),
      ].map((node) => {
        const rect = node.getBoundingClientRect()
        return {
          left: rect.left - bounds.left,
          right: rect.right - bounds.left,
          y: rect.top - bounds.top + rect.height / 2,
        }
      })
      cancelAnimationFrame(frame)
      draw(performance.now())
    }
    const observer = new ResizeObserver(measure)
    observer.observe(root)
    const intersection = new IntersectionObserver(([entry]) => {
      visible = entry.isIntersecting
      cancelAnimationFrame(frame)
      if (visible) draw(performance.now())
    })
    intersection.observe(root)
    const themeObserver = new MutationObserver(measure)
    themeObserver.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ['class'],
    })
    const resume = () => {
      measure()
    }
    motion.addEventListener('change', resume)
    document.addEventListener('visibilitychange', resume)
    measure()
    return () => {
      cancelAnimationFrame(frame)
      observer.disconnect()
      intersection.disconnect()
      themeObserver.disconnect()
      motion.removeEventListener('change', resume)
      document.removeEventListener('visibilitychange', resume)
    }
  }, [selected, paused])

  return (
    <div className='gateway-network' ref={rootRef}>
      <canvas
        ref={canvasRef}
        className='gateway-network-canvas'
        aria-hidden='true'
      />
      <div className='gateway-container gateway-network-layout'>
        <div
          className='gateway-map'
          role='group'
          aria-label={t('Model routing illustration')}
        >
          <div className='gateway-map-caption'>
            API GATEWAY <span aria-hidden='true'>/</span> MODEL NETWORK
          </div>
          <div className='routing-source' data-routing-node>
            <KeyRound size={19} aria-hidden='true' />
            <span>API</span>
          </div>
          <div className='routing-hub' data-routing-node>
            <div className='routing-hub-chip'>
              <Cpu size={34} strokeWidth={1.3} aria-hidden='true' />
            </div>
            <strong>{props.systemName}</strong>
            <span>{t('Intelligent routing')}</span>
          </div>
          {providers.map((provider, index) => (
            <Button
              key={provider.name}
              variant='outline'
              className={`routing-provider routing-provider-${index}`}
              data-routing-node
              aria-label={t('Select {{provider}} route', {
                provider: provider.name,
              })}
              aria-pressed={selected === index}
              onClick={() => setSelected(index)}
            >
              <span aria-hidden='true'>{getLobeIcon(provider.icon, 24)}</span>
              <span>{provider.name}</span>
            </Button>
          ))}
          <div className='gateway-map-footer'>
            <span className='gateway-protocol-label'>
              OpenAI / Anthropic / Gemini
            </span>
            <Tooltip>
              <TooltipTrigger
                render={
                  <Button
                    variant='ghost'
                    size='icon'
                    className='gateway-animation-toggle'
                    aria-label={
                      paused
                        ? t('Resume routing animation')
                        : t('Pause routing animation')
                    }
                    aria-pressed={paused}
                    onClick={() => setPaused(!paused)}
                  >
                    {paused ? (
                      <Play aria-hidden='true' />
                    ) : (
                      <Pause aria-hidden='true' />
                    )}
                  </Button>
                }
              />
              <TooltipContent>
                {paused
                  ? t('Resume routing animation')
                  : t('Pause routing animation')}
              </TooltipContent>
            </Tooltip>
          </div>
        </div>
      </div>
    </div>
  )
}
