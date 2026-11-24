import {
  Activity,
  AlertTriangle,
  CheckCircle2,
  ChevronLeft,
  ChevronRight,
  Clock,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { StatusBadge, type StatusVariant } from '@/components/status-badge'
import { Button } from '@/components/ui/button'

const health = {
  healthy: CheckCircle2,
  abnormal: AlertTriangle,
  slow: Clock,
  waiting: Activity,
}

const colors: Record<string, StatusVariant> = {
  healthy: 'success',
  succeeded: 'success',
  sent: 'success',
  abnormal: 'danger',
  failed: 'danger',
  slow: 'warning',
  uncertain: 'warning',
  queued: 'info',
  submitting: 'info',
  polling: 'info',
  sending: 'info',
}

export function Status(props: { state: string }) {
  const { t } = useTranslation()
  const Icon = health[props.state as keyof typeof health] ?? Activity
  return (
    <StatusBadge
      copyable={false}
      icon={Icon}
      label={t(props.state)}
      variant={colors[props.state] ?? 'neutral'}
    />
  )
}

export function Pages(props: {
  page: number
  hasMore: boolean
  onPage: (page: number) => void
}) {
  const { t } = useTranslation()
  return (
    <div className='flex items-center justify-end gap-2 py-3'>
      <Button
        size='icon'
        variant='outline'
        aria-label={t('Previous page')}
        disabled={props.page <= 1}
        onClick={() => props.onPage(props.page - 1)}
      >
        <ChevronLeft aria-hidden='true' />
      </Button>
      <span className='text-muted-foreground min-w-8 text-center tabular-nums'>
        {props.page}
      </span>
      <Button
        size='icon'
        variant='outline'
        aria-label={t('Next page')}
        disabled={!props.hasMore}
        onClick={() => props.onPage(props.page + 1)}
      >
        <ChevronRight aria-hidden='true' />
      </Button>
    </div>
  )
}
