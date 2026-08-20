/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { zodResolver } from '@hookform/resolvers/zod'
import {
  InformationCircleIcon,
  SaveIcon,
  UndoIcon,
} from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useQueryClient } from '@tanstack/react-query'
import type { TFunction } from 'i18next'
import { useEffect, useMemo, useRef } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import * as z from 'zod'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import {
  Field,
  FieldError,
  FieldGroup,
  FieldLabel,
  FieldLegend,
  FieldSet,
} from '@/components/ui/field'
import { Form, FormField } from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Spinner } from '@/components/ui/spinner'

import {
  SettingsPageActionsPortal,
  SettingsPageTitleStatusPortal,
} from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useUpdateOption } from '../hooks/use-update-option'
import { safeNumberFieldProps } from '../utils/numeric-field'
import {
  applySeedancePricingFormValues,
  buildSeedancePricingFormValues,
  MAX_SEEDANCE_VIDEO_PRICE_CNY,
  parseSeedanceVideoPrices,
  SEEDANCE_FAST_MODEL,
  SEEDANCE_PRICE_ROWS,
  SEEDANCE_STANDARD_MODEL,
  SEEDANCE_VIDEO_PRICING_OPTION_KEY,
  type SeedancePricingFormValues,
} from './seedance-video-pricing'

function createSeedancePricingSchema(t: TFunction) {
  const priceSchema = z
    .number()
    .finite(t('Each price must be a finite number.'))
    .positive(t('Each price must be greater than 0.'))
    .max(
      MAX_SEEDANCE_VIDEO_PRICE_CNY,
      t('Each price must not exceed 1,000,000.')
    )

  return z.object({
    rows: z
      .array(
        z.object({
          without_video: priceSchema,
          with_video: priceSchema,
        })
      )
      .length(SEEDANCE_PRICE_ROWS.length),
  })
}

const modelGroups = [SEEDANCE_STANDARD_MODEL, SEEDANCE_FAST_MODEL] as const

type Props = {
  defaultValue: string
}

export function SeedanceVideoPricingCard({ defaultValue }: Props) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const updateOption = useUpdateOption()
  const parsedDefaults = useMemo(
    () => parseSeedanceVideoPrices(defaultValue),
    [defaultValue]
  )
  const formDefaults = useMemo(
    () => buildSeedancePricingFormValues(parsedDefaults),
    [parsedDefaults]
  )
  const schema = useMemo(() => createSeedancePricingSchema(t), [t])
  const form = useForm<
    SeedancePricingFormValues,
    unknown,
    SeedancePricingFormValues
  >({
    resolver: zodResolver(schema),
    defaultValues: formDefaults,
  })
  const baselineSerializedRef = useRef(defaultValue)

  useEffect(() => {
    if (defaultValue === baselineSerializedRef.current) return

    baselineSerializedRef.current = defaultValue
    form.reset(formDefaults)
  }, [defaultValue, form, formDefaults])

  const onSubmit = async (values: SeedancePricingFormValues) => {
    const prices = applySeedancePricingFormValues(values)
    const serialized = JSON.stringify(prices)

    try {
      const response = await updateOption.mutateAsync({
        key: SEEDANCE_VIDEO_PRICING_OPTION_KEY,
        value: serialized,
      })
      if (!response.success) return

      baselineSerializedRef.current = serialized
      form.reset(values)
      await queryClient.invalidateQueries({ queryKey: ['pricing'] })
    } catch {
      // useUpdateOption reports the request error to the operator.
    }
  }

  return (
    <SettingsSection title={t('Seedance CNY Pricing')}>
      <Form {...form}>
        <form onSubmit={form.handleSubmit(onSubmit)}>
          <SettingsPageTitleStatusPortal>
            <span className='text-muted-foreground text-xs font-normal'>
              {t('CNY / 1M tokens')}
            </span>
          </SettingsPageTitleStatusPortal>
          <SettingsPageActionsPortal>
            <Button
              type='button'
              size='sm'
              variant='outline'
              onClick={() => form.reset(formDefaults)}
              disabled={!form.formState.isDirty || updateOption.isPending}
            >
              <HugeiconsIcon icon={UndoIcon} strokeWidth={2} />
              <span>{t('Reset')}</span>
            </Button>
            <Button
              type='submit'
              size='sm'
              disabled={!form.formState.isDirty || updateOption.isPending}
            >
              {updateOption.isPending ? (
                <Spinner aria-hidden='true' />
              ) : (
                <HugeiconsIcon icon={SaveIcon} strokeWidth={2} />
              )}
              <span>
                {updateOption.isPending
                  ? t('Saving...')
                  : t('Save Seedance prices')}
              </span>
            </Button>
          </SettingsPageActionsPortal>

          <Card>
            <CardHeader>
              <CardTitle>{t('Seedance CNY Pricing')}</CardTitle>
              <CardDescription>
                {t(
                  'Set the customer base price per million video tokens for each model and resolution.'
                )}
              </CardDescription>
            </CardHeader>
            <CardContent>
              <FieldGroup>
                {modelGroups.map((model) => {
                  const rows = SEEDANCE_PRICE_ROWS.map((row, index) => ({
                    ...row,
                    index,
                  })).filter((row) => row.model === model)

                  return (
                    <FieldSet
                      key={model}
                      className='bg-muted/20 rounded-lg border p-3 sm:p-4'
                    >
                      <FieldLegend className='w-full'>
                        <span className='flex min-w-0 flex-col gap-0.5'>
                          <span>
                            {model === SEEDANCE_STANDARD_MODEL
                              ? t('Seedance 2.0 Standard')
                              : t('Seedance 2.0 Fast')}
                          </span>
                          <code className='text-muted-foreground text-xs font-normal break-all'>
                            {model}
                          </code>
                        </span>
                      </FieldLegend>

                      <div className='hidden grid-cols-[minmax(7rem,1fr)_minmax(9rem,1fr)_minmax(9rem,1fr)] gap-3 px-3 text-xs font-medium sm:grid'>
                        <span>{t('Resolution')}</span>
                        <span>{t('Without input video')}</span>
                        <span>{t('With input video')}</span>
                      </div>

                      <FieldGroup className='gap-3'>
                        {rows.map((row) => {
                          let resolutionLabel: string = row.resolution
                          if (row.resolution === 'default') {
                            resolutionLabel = t('All supported resolutions')
                          } else if (row.resolution === '4k') {
                            resolutionLabel = '4K'
                          }

                          return (
                            <div
                              key={`${row.model}-${row.resolution}`}
                              className='bg-background grid gap-3 rounded-lg border p-3 sm:grid-cols-[minmax(7rem,1fr)_minmax(9rem,1fr)_minmax(9rem,1fr)] sm:items-start'
                            >
                              <div className='flex min-h-9 items-center'>
                                <span className='text-sm font-medium'>
                                  {resolutionLabel}
                                </span>
                              </div>

                              <FormField
                                control={form.control}
                                name={`rows.${row.index}.without_video`}
                                render={({ field, fieldState }) => {
                                  const inputId = `seedance-${row.index}-without-video`
                                  return (
                                    <Field data-invalid={fieldState.invalid}>
                                      <FieldLabel
                                        htmlFor={inputId}
                                        className='sm:sr-only'
                                      >
                                        <span
                                          aria-hidden='true'
                                          className='sm:hidden'
                                        >
                                          {t('Without input video')}
                                        </span>
                                        <span className='sr-only'>
                                          {resolutionLabel}:{' '}
                                          {t('Without input video')}
                                        </span>
                                      </FieldLabel>
                                      <Input
                                        {...safeNumberFieldProps(field)}
                                        id={inputId}
                                        type='number'
                                        min={0.01}
                                        max={MAX_SEEDANCE_VIDEO_PRICE_CNY}
                                        step={0.01}
                                        inputMode='decimal'
                                        aria-invalid={fieldState.invalid}
                                        aria-describedby={
                                          fieldState.invalid
                                            ? `${inputId}-error`
                                            : undefined
                                        }
                                      />
                                      <FieldError
                                        id={`${inputId}-error`}
                                        errors={[fieldState.error]}
                                      />
                                    </Field>
                                  )
                                }}
                              />

                              <FormField
                                control={form.control}
                                name={`rows.${row.index}.with_video`}
                                render={({ field, fieldState }) => {
                                  const inputId = `seedance-${row.index}-with-video`
                                  return (
                                    <Field data-invalid={fieldState.invalid}>
                                      <FieldLabel
                                        htmlFor={inputId}
                                        className='sm:sr-only'
                                      >
                                        <span
                                          aria-hidden='true'
                                          className='sm:hidden'
                                        >
                                          {t('With input video')}
                                        </span>
                                        <span className='sr-only'>
                                          {resolutionLabel}:{' '}
                                          {t('With input video')}
                                        </span>
                                      </FieldLabel>
                                      <Input
                                        {...safeNumberFieldProps(field)}
                                        id={inputId}
                                        type='number'
                                        min={0.01}
                                        max={MAX_SEEDANCE_VIDEO_PRICE_CNY}
                                        step={0.01}
                                        inputMode='decimal'
                                        aria-invalid={fieldState.invalid}
                                        aria-describedby={
                                          fieldState.invalid
                                            ? `${inputId}-error`
                                            : undefined
                                        }
                                      />
                                      <FieldError
                                        id={`${inputId}-error`}
                                        errors={[fieldState.error]}
                                      />
                                    </Field>
                                  )
                                }}
                              />
                            </div>
                          )
                        })}
                      </FieldGroup>
                    </FieldSet>
                  )
                })}
              </FieldGroup>
            </CardContent>
            <CardFooter>
              <Alert className='w-full'>
                <HugeiconsIcon icon={InformationCircleIcon} strokeWidth={2} />
                <AlertTitle>{t('Billing behavior')}</AlertTitle>
                <AlertDescription>
                  {t(
                    'Group ratios are applied after these base prices. Tasks already submitted keep the price snapshot captured at submission.'
                  )}
                </AlertDescription>
              </Alert>
            </CardFooter>
          </Card>
        </form>
      </Form>
    </SettingsSection>
  )
}
