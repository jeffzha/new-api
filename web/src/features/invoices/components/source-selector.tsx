import { useTranslation } from 'react-i18next'

import { EmptyState } from '@/components/empty-state'
import { Badge } from '@/components/ui/badge'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'

import type { InvoiceSource } from '../types'

function money(cents: number) {
  return `¥${(cents / 100).toFixed(2)}`
}

export function SourceSelector(props: {
  sources: InvoiceSource[]
  amounts: Record<number, string>
  errors?: Record<number, string>
  onChange: (id: number, value: string) => void
  onToggle: (id: number, selected: boolean) => void
}) {
  const { t } = useTranslation()
  return (
    <div className='space-y-3'>
      {props.sources.map((source) => {
        const value = props.amounts[source.topup_id] ?? ''
        const selected = props.amounts[source.topup_id] != null
        const error = props.errors?.[source.topup_id]
        return (
          <div
            key={source.topup_id}
            className='flex flex-col gap-3 rounded-lg border p-3 sm:flex-row sm:items-center'
          >
            <Checkbox
              checked={selected}
              onCheckedChange={(checked) =>
                props.onToggle(source.topup_id, Boolean(checked))
              }
              aria-label={t('Select invoice source {{tradeNo}}', {
                tradeNo: source.trade_no,
              })}
            />
            <div className='min-w-0 flex-1'>
              <div className='flex flex-wrap items-center gap-2'>
                <span className='font-medium'>
                  {t('Top-up source')} #{source.topup_id}
                </span>
                <Badge variant='outline'>{source.currency_code}</Badge>
              </div>
              <p className='text-muted-foreground truncate text-xs'>
                {source.trade_no}
              </p>
              <p className='text-muted-foreground text-xs'>
                {t('Available {{amount}}', {
                  amount: money(source.available_cents),
                })}
              </p>
            </div>
            <div className='space-y-1 sm:w-48'>
              <div className='flex items-center gap-2'>
                <span className='text-muted-foreground'>¥</span>
                <Input
                  inputMode='decimal'
                  value={value}
                  onChange={(event) =>
                    props.onChange(source.topup_id, event.target.value)
                  }
                  disabled={!selected}
                  aria-invalid={Boolean(error)}
                  aria-describedby={
                    error
                      ? `invoice-source-error-${source.topup_id}`
                      : undefined
                  }
                  aria-label={t('Invoice amount for {{tradeNo}}', {
                    tradeNo: source.trade_no,
                  })}
                />
              </div>
              {error && (
                <p
                  id={`invoice-source-error-${source.topup_id}`}
                  className='text-destructive text-xs'
                >
                  {t(error)}
                </p>
              )}
            </div>
          </div>
        )
      })}
      {props.sources.length === 0 && (
        <EmptyState
          title={t('No eligible top-up sources')}
          description={t('Only verified CNY payments can be invoiced.')}
        />
      )}
    </div>
  )
}
