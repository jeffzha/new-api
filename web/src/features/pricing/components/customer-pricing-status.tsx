import { BadgePercent, Percent, ShieldCheck, Sparkles, Tag } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Dialog } from '@/components/dialog'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

import type { PricingModel } from '../types'

export function CustomerPricingNotice(props: { models?: PricingModel[] }) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const models = (props.models ?? [])
    .filter((model) => model.sales_bps != null)
    .sort((a, b) => a.model_name.localeCompare(b.model_name))

  return (
    <>
      <Alert className='border-primary/25 bg-gradient-to-r from-primary/[0.08] via-primary/[0.04] to-background shadow-sm'>
        <Tag className='text-primary' aria-hidden='true' />
        <AlertDescription className='flex flex-wrap items-center gap-x-3 gap-y-1.5'>
          <span className='font-semibold text-foreground'>
            {t('You are using agency-exclusive pricing.')}
          </span>
          <span className='text-muted-foreground'>
            {t(
              "Prices are calculated using your agency's sales policy. Switching groups only filters available models and does not change your exclusive price."
            )}
          </span>
          <Button
            type='button'
            variant='link'
            size='sm'
            className='h-auto gap-1 px-0 font-semibold'
            onClick={() => setOpen(true)}
          >
            <Sparkles className='size-4' aria-hidden='true' />
            {t('View exclusive pricing')}
          </Button>
        </AlertDescription>
      </Alert>

      <Dialog
        open={open}
        onOpenChange={setOpen}
        title={
          <span className='flex items-center gap-2.5 text-lg font-semibold'>
            <span className='flex size-9 items-center justify-center rounded-xl bg-primary/10 text-primary'>
              <BadgePercent className='size-5' aria-hidden='true' />
            </span>
            {t('Agency-exclusive pricing details')}
          </span>
        }
        description={t(
          'The coefficient is relative to the platform standard price. For example, 80% means you pay 80% of the standard price.'
        )}
        contentHeight='min(520px, calc(100dvh - 14rem))'
        contentClassName='sm:max-w-4xl sm:rounded-2xl sm:p-7'
        headerClassName='border-b pb-4'
        descriptionClassName='max-w-2xl leading-6'
        bodyClassName='h-full space-y-4 py-2'
      >
          <div className='grid gap-3 sm:grid-cols-2'>
            <div className='flex items-center gap-3 rounded-xl border bg-muted/35 p-3.5'>
              <span className='flex size-9 items-center justify-center rounded-lg bg-primary/10 text-primary'>
                <Tag className='size-4' aria-hidden='true' />
              </span>
              <div>
                <p className='text-xs text-muted-foreground'>{t('Exclusive models')}</p>
                <p className='font-semibold'>{t('{{count}} models', { count: models.length })}</p>
              </div>
            </div>
            <div className='flex items-center gap-3 rounded-xl border bg-muted/35 p-3.5'>
              <span className='flex size-9 items-center justify-center rounded-lg bg-emerald-500/10 text-emerald-600 dark:text-emerald-400'>
                <ShieldCheck className='size-4' aria-hidden='true' />
              </span>
              <div>
                <p className='text-xs text-muted-foreground'>{t('Pricing basis')}</p>
                <p className='font-semibold'>{t('Platform standard price × agency sales coefficient')}</p>
              </div>
            </div>
          </div>
          <div
            role='region'
            aria-label={t('Agency pricing model list')}
            className='min-h-0 overflow-x-auto rounded-xl border bg-background shadow-xs'
          >
            <Table>
              <TableHeader className='sticky top-0 z-10 bg-muted/95 backdrop-blur'>
                <TableRow>
                  <TableHead className='min-w-64'>{t('Model')}</TableHead>
                  <TableHead className='min-w-40 text-right'>{t('Exclusive price coefficient')}</TableHead>
                  <TableHead className='min-w-40 text-right'>{t('Compared with standard price')}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {models.length > 0 ? (
                  models.map((model) => {
                    const salesBps = model.sales_bps ?? 10000
                    const coefficient = salesBps / 100
                    const discount = Math.max(0, 100 - coefficient)
                    return (
                      <TableRow key={model.model_name}>
                        <TableCell className='max-w-[28rem] truncate py-4 font-medium'>
                          {model.model_name}
                        </TableCell>
                        <TableCell className='text-right'>
                          <Badge variant='secondary' className='gap-1.5 rounded-full px-3 py-1 font-semibold text-primary'>
                            <Percent aria-hidden='true' />
                            {coefficient.toFixed(2)}%
                          </Badge>
                        </TableCell>
                        <TableCell className='text-right font-medium text-emerald-600 dark:text-emerald-400'>
                          {t('Save {{value}}%', { value: discount.toFixed(2) })}
                        </TableCell>
                      </TableRow>
                    )
                  })
                ) : (
                  <TableRow>
                    <TableCell colSpan={3} className='text-muted-foreground py-8 text-center'>
                      {t('No agency model discounts available.')}
                    </TableCell>
                  </TableRow>
                )}
              </TableBody>
            </Table>
          </div>
      </Dialog>
    </>
  )
}

export function PricingUnavailable(props: { onRetry: () => void }) {
  const { t } = useTranslation()
  return (
    <Alert variant='destructive'>
      <AlertDescription>
        {t('Pricing is temporarily unavailable. Please try again.')}
        <Button variant='outline' size='sm' onClick={props.onRetry}>
          {t('Retry')}
        </Button>
      </AlertDescription>
    </Alert>
  )
}
