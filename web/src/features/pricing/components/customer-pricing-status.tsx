import { Percent, Tag } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
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
      <Alert className='border-primary/25 bg-primary/[0.04]'>
        <Tag className='text-primary' aria-hidden='true' />
        <AlertDescription className='flex flex-wrap items-center gap-x-2 gap-y-1'>
          <span>
            {t('You are using agency-exclusive pricing. Click here for details.')}
          </span>
          <span className='text-muted-foreground/80'>
            {t(
              'Prices include your agency sales policy. Group selection changes model availability, not your sales rate.'
            )}
          </span>
          <Button
            type='button'
            variant='link'
            size='sm'
            className='h-auto px-0 font-semibold'
            onClick={() => setOpen(true)}
          >
            {t('View agency pricing details')}
          </Button>
        </AlertDescription>
      </Alert>

      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className='max-h-[min(720px,calc(100dvh-2rem))] max-w-3xl overflow-hidden'>
          <DialogHeader>
            <DialogTitle>{t('Agency pricing details')}</DialogTitle>
            <DialogDescription>
              {t(
                'These prices use the sales coefficient configured for your agency. A coefficient of 90% means you pay 90% of the standard price.'
              )}
            </DialogDescription>
          </DialogHeader>
          <div className='min-h-0 overflow-auto rounded-lg border'>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t('Model')}</TableHead>
                  <TableHead className='text-right'>{t('Discount rate')}</TableHead>
                  <TableHead className='text-right'>{t('Savings')}</TableHead>
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
                        <TableCell className='max-w-[24rem] truncate font-medium'>
                          {model.model_name}
                        </TableCell>
                        <TableCell className='text-right'>
                          <Badge variant='secondary' className='gap-1'>
                            <Percent aria-hidden='true' />
                            {coefficient.toFixed(2)}%
                          </Badge>
                        </TableCell>
                        <TableCell className='text-right text-emerald-600 dark:text-emerald-400'>
                          {discount.toFixed(2)}%
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
        </DialogContent>
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
