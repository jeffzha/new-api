/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { CalendarDays } from 'lucide-react'
import { Controller, type UseFormReturn } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

import { CREDIT_REPAYMENT_DAY } from '../lib/format'
import type { CreditTermsFormValues } from '../lib/terms-schema'

export function CreditTermsFields(props: {
  form: UseFormReturn<CreditTermsFormValues>
  id: string
  disabled?: boolean
}) {
  const { t } = useTranslation()
  const errors = props.form.formState.errors
  return (
    <fieldset
      disabled={props.disabled}
      className='grid min-w-0 gap-3 sm:grid-cols-2'
    >
      <div className='space-y-1.5'>
        <Label htmlFor={`${props.id}-limit`}>{t('Credit limit (CNY)')}</Label>
        <Input
          id={`${props.id}-limit`}
          inputMode='decimal'
          aria-invalid={Boolean(errors.limit)}
          aria-describedby={
            errors.limit ? `${props.id}-limit-error` : undefined
          }
          {...props.form.register('limit')}
        />
        {errors.limit && (
          <p
            id={`${props.id}-limit-error`}
            role='alert'
            className='text-destructive text-xs'
          >
            {t('Enter a valid credit limit and payment term.')}
          </p>
        )}
      </div>
      <div className='space-y-1.5'>
        <Label htmlFor={`${props.id}-term`}>{t('Payment term (days)')}</Label>
        <Input
          id={`${props.id}-term`}
          type='number'
          min={0}
          max={365}
          aria-invalid={Boolean(errors.termDays)}
          aria-describedby={
            errors.termDays ? `${props.id}-term-error` : undefined
          }
          {...props.form.register('termDays')}
        />
        {errors.termDays && (
          <p
            id={`${props.id}-term-error`}
            role='alert'
            className='text-destructive text-xs'
          >
            {t('Enter a valid credit limit and payment term.')}
          </p>
        )}
      </div>
      <div className='space-y-1.5'>
        <Label>{t('Repayment day')}</Label>
        <div className='border-input bg-muted/40 flex min-h-8 items-center gap-2 rounded-md border px-2.5 text-sm'>
          <CalendarDays className='size-4 shrink-0' />
          {t('Every month on day {{day}}', { day: CREDIT_REPAYMENT_DAY })}
        </div>
      </div>
      <div className='space-y-1.5'>
        <Label htmlFor={`${props.id}-status`}>{t('Credit status')}</Label>
        <Controller
          control={props.form.control}
          name='status'
          render={({ field }) => (
            <Select
              value={field.value}
              onValueChange={field.onChange}
              disabled={props.disabled}
            >
              <SelectTrigger id={`${props.id}-status`} className='w-full'>
                <SelectValue>
                  {t(field.value === 'active' ? 'Active' : 'Suspended')}
                </SelectValue>
              </SelectTrigger>
              <SelectContent>
                <SelectItem value='active'>{t('Active')}</SelectItem>
                <SelectItem value='suspended'>{t('Suspended')}</SelectItem>
              </SelectContent>
            </Select>
          )}
        />
      </div>
      <Controller
        control={props.form.control}
        name='emailEnabled'
        render={({ field }) => (
          <label className='flex items-center gap-2 sm:col-span-2'>
            <Checkbox
              checked={field.value}
              onCheckedChange={(checked) => field.onChange(checked === true)}
              disabled={props.disabled}
            />
            <span className='text-sm'>{t('Send statement emails')}</span>
          </label>
        )}
      />
      {errors.root && (
        <p role='alert' className='text-destructive text-sm sm:col-span-2'>
          {t('Failed to save credit terms')}
        </p>
      )}
    </fieldset>
  )
}
