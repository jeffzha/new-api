/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { Pause, Play } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'

interface ShowcaseVideoProps {
  src: string
  poster: string
  label: string
}

export function ShowcaseVideo(props: ShowcaseVideoProps) {
  const { t } = useTranslation()
  const videoRef = useRef<HTMLVideoElement>(null)
  const [playing, setPlaying] = useState(false)
  const [failed, setFailed] = useState(false)
  const [requested, setRequested] = useState<boolean | null>(null)

  useEffect(() => {
    const video = videoRef.current
    if (!video || failed) return
    const motion = window.matchMedia('(prefers-reduced-motion: reduce)')
    let visible = false
    let disposed = false
    let revision = 0
    const sync = () => {
      const version = ++revision
      const shouldPlay =
        visible && !document.hidden && (requested ?? !motion.matches)
      if (!shouldPlay) {
        video.pause()
        return
      }
      void video
        .play()
        .then(() => {
          // A pending play request must not restart a hidden or unmounted video.
          if (disposed || version !== revision) video.pause()
        })
        .catch(() => {
          if (!disposed && version === revision) setPlaying(false)
        })
    }
    let observer: IntersectionObserver | null = null
    if (typeof IntersectionObserver === 'undefined') {
      visible = true
      sync()
    } else {
      observer = new IntersectionObserver(
        ([entry]) => {
          visible = entry.isIntersecting
          sync()
        },
        { threshold: 0.15 }
      )
      observer.observe(video)
    }
    document.addEventListener('visibilitychange', sync)
    motion.addEventListener('change', sync)
    return () => {
      disposed = true
      observer?.disconnect()
      document.removeEventListener('visibilitychange', sync)
      motion.removeEventListener('change', sync)
      video.pause()
    }
  }, [requested, failed])

  const action = playing ? t('Pause preview') : t('Play preview')
  return (
    <div className='gateway-video'>
      {failed ? (
        <img src={props.poster} alt={props.label} width='1280' height='720' />
      ) : (
        <video
          ref={videoRef}
          src={props.src}
          poster={props.poster}
          aria-label={props.label}
          muted
          loop
          playsInline
          preload='none'
          onPlay={() => setPlaying(true)}
          onPause={() => setPlaying(false)}
          onError={() => setFailed(true)}
        />
      )}
      {failed ? (
        <span className='gateway-video-fallback'>
          {t('Video unavailable. Showing preview image.')}
        </span>
      ) : (
        <Tooltip>
          <TooltipTrigger
            render={
              <Button
                variant='ghost'
                size='icon'
                className='gateway-video-toggle'
                aria-label={`${action}: ${props.label}`}
                onClick={() => setRequested(!playing)}
              />
            }
          >
            {playing ? (
              <Pause size={18} aria-hidden='true' />
            ) : (
              <Play size={18} aria-hidden='true' />
            )}
          </TooltipTrigger>
          <TooltipContent>{action}</TooltipContent>
        </Tooltip>
      )}
    </div>
  )
}
