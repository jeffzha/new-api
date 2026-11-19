/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { toIntlLocale } from '@/i18n/languages'

import { ModelPriceCell } from '../components/model-price-cell'
import type { PricingModel } from '../types'
import type { ListPriceStatus } from './types'

export function ListPriceNotice(props: {
  models: PricingModel[]
  status?: ListPriceStatus
  priceRate: number
  usdExchangeRate: number
}) {
  const { t, i18n } = useTranslation()
  const [open, setOpen] = useState(false)
  const hasSnapshot = Boolean(props.status?.version)
  const sourceCount = props.models.filter(
    (model) => model.list_price?.source === 'ops'
  ).length
  const options = {
    priceRate: props.priceRate,
    usdExchangeRate: props.usdExchangeRate,
  }

  return (
    <>
      <Alert>
        <AlertDescription className='flex flex-wrap items-center gap-x-3 gap-y-2'>
          <span>
            {t(
              'The model catalog shows list prices before discounts. Actual charges are shown in usage logs.'
            )}
          </span>
          <span className='text-muted-foreground'>
            {t(
              'Operations list prices: {{count}} models. Other models use local list prices.',
              { count: sourceCount }
            )}
          </span>
          {props.status?.stale && (
            <span className='text-amber-700 dark:text-amber-300'>
              {hasSnapshot
                ? t(
                    'Price refresh is delayed. Previously loaded list prices are still shown.'
                  )
                : t(
                    'Operations prices are not available yet. Local list prices are shown.'
                  )}
            </span>
          )}
          <Button
            type='button'
            variant='outline'
            size='sm'
            onClick={() => setOpen(true)}
          >
            {t('Compare list prices')}
          </Button>
        </AlertDescription>
      </Alert>
      <Dialog
        open={open}
        onOpenChange={setOpen}
        title={t('List price comparison')}
        description={t(
          'Compare list prices before discounts. Tiered and task prices require checking all conditions in model details.'
        )}
        contentClassName='sm:max-w-6xl'
        contentHeight='min(540px, calc(100dvh - 14rem))'
      >
        {props.status?.checked_at && (
          <p className='text-muted-foreground mb-3 text-sm'>
            {t('Last price check')}:{' '}
            {new Date(props.status.checked_at).toLocaleString(
              toIntlLocale(i18n.language)
            )}
          </p>
        )}
        <div
          role='region'
          aria-label={t('List price comparison')}
          className='overflow-x-auto rounded-lg border'
        >
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className='min-w-48'>{t('Model')}</TableHead>
                <TableHead className='min-w-32'>{t('Price source')}</TableHead>
                <TableHead className='min-w-56'>
                  {t('Displayed list price')}
                </TableHead>
                <TableHead className='min-w-56'>
                  {t('Local list price')}
                </TableHead>
                <TableHead className='min-w-48'>
                  {t('Comparison result')}
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {props.models.map((model) => {
                const info = model.list_price
                let comparison = t('Check pricing conditions')
                if (info?.comparison === 'match') {
                  comparison = t('Unit prices match')
                }
                if (info?.comparison === 'differ') {
                  comparison = t('Unit prices differ')
                }
                if (info?.comparison === 'unavailable') {
                  comparison = t('Operations price not provided')
                }
                if (info?.comparison === 'unsupported') {
                  comparison = t('Price format needs confirmation')
                }
                if (info?.comparison === 'conversion_mismatch') {
                  comparison = t('Currency conversion settings differ')
                }
                const local = {
                  ...(info?.local ?? model),
                  sales_bps: undefined,
                  group_ratio: Object.fromEntries(
                    model.enable_groups.map((group) => [group, 1])
                  ),
                }
                return (
                  <TableRow key={model.model_name}>
                    <TableCell className='max-w-72 font-medium break-all'>
                      {model.model_name}
                    </TableCell>
                    <TableCell>
                      <Badge variant='secondary'>
                        {info?.source === 'ops'
                          ? t('Operations platform')
                          : t('Local platform')}
                      </Badge>
                    </TableCell>
                    <TableCell>
                      <ModelPriceCell
                        model={model}
                        options={options}
                        showExpression={false}
                      />
                    </TableCell>
                    <TableCell>
                      <ModelPriceCell
                        model={local}
                        options={options}
                        showExpression={false}
                      />
                    </TableCell>
                    <TableCell className='text-sm'>{comparison}</TableCell>
                  </TableRow>
                )
              })}
              {props.models.length === 0 && (
                <TableRow>
                  <TableCell
                    colSpan={5}
                    className='text-muted-foreground py-8 text-center'
                  >
                    {t('No models available')}
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
