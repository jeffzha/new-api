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
import { useEffect, useMemo, useRef, type FormEvent } from 'react'
import { useForm, useFormState } from 'react-hook-form'
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
  parseSeedanceRequestPrices,
  SEEDANCE_25_MODEL,
  SEEDANCE_FAST_MODEL,
  SEEDANCE_MINI_MODEL,
  SEEDANCE_PRICE_ROWS,
  SEEDANCE_REQUEST_PRICE_ROWS,
  SEEDANCE_REQUEST_PRICING_OPTION_KEY,
  SEEDANCE_STANDARD_MODEL,
  SEEDANCE_VIDEO_PRICING_OPTION_KEY,
  type SeedancePricingFormValues,
} from './seedance-video-pricing'

type PricingFormValues = SeedancePricingFormValues & {
  requests: SeedancePricingFormValues
}

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
    requests: z.object({
      rows: z.array(z.object({ without_video: priceSchema, with_video: priceSchema }))
        .length(SEEDANCE_REQUEST_PRICE_ROWS.length),
    }),
  })
}

const modelGroups = [
  SEEDANCE_STANDARD_MODEL,
  SEEDANCE_FAST_MODEL,
  SEEDANCE_25_MODEL,
] as const

type Props = {
  defaultValue: string
  requestDefaultValue: string
}

export function SeedanceVideoPricingCard(props: Props) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const updateOption = useUpdateOption()
  const parsedDefaults = useMemo(
    () => parseSeedanceVideoPrices(props.defaultValue),
    [props.defaultValue]
  )
  const formDefaults = useMemo(
    () => ({
      ...buildSeedancePricingFormValues(parsedDefaults),
      requests: parseSeedanceRequestPrices(props.requestDefaultValue),
    }),
    [parsedDefaults, props.requestDefaultValue]
  )
  const schema = useMemo(() => createSeedancePricingSchema(t), [t])
  const form = useForm<
    PricingFormValues,
    unknown,
    PricingFormValues
  >({
    resolver: zodResolver(schema),
    defaultValues: formDefaults,
  })
  const { control, handleSubmit, reset, resetField } = form
  const baselineSerializedRef = useRef(props.defaultValue)
  const requestBaselineRef = useRef(props.requestDefaultValue)
  const { dirtyFields, isDirty } = useFormState({ control })

  useEffect(() => {
    if (props.defaultValue !== baselineSerializedRef.current) {
      baselineSerializedRef.current = props.defaultValue
      resetField('rows', { defaultValue: formDefaults.rows })
    }
    if (props.requestDefaultValue !== requestBaselineRef.current) {
      requestBaselineRef.current = props.requestDefaultValue
      resetField('requests', { defaultValue: formDefaults.requests })
    }
  }, [props.defaultValue, props.requestDefaultValue, resetField, formDefaults])

  const onSubmit = async (values: PricingFormValues) => {
    const tokenDirty = !!dirtyFields.rows
    const requestDirty = !!dirtyFields.requests

    try {
      if (tokenDirty) {
        const serialized = JSON.stringify(applySeedancePricingFormValues(values, parsedDefaults))
        const response = await updateOption.mutateAsync({
          key: SEEDANCE_VIDEO_PRICING_OPTION_KEY, value: serialized,
        })
        if (!response.success) return
        baselineSerializedRef.current = serialized
        resetField('rows', { defaultValue: values.rows })
        await queryClient.invalidateQueries({ queryKey: ['pricing'] })
      }
      if (requestDirty) {
        const serialized = JSON.stringify({ [SEEDANCE_MINI_MODEL]: values.requests.rows[0] })
        const response = await updateOption.mutateAsync({
          key: SEEDANCE_REQUEST_PRICING_OPTION_KEY, value: serialized,
        })
        if (!response.success) return
        requestBaselineRef.current = serialized
        resetField('requests', { defaultValue: values.requests })
        await queryClient.invalidateQueries({ queryKey: ['pricing'] })
      }
    } catch {
      // useUpdateOption reports the request error to the operator.
    }
  }

  const submitForm = (event: FormEvent<HTMLFormElement>) => {
    void handleSubmit(onSubmit)(event)
  }

  return (
    <SettingsSection title={t('Seedance CNY Pricing')}>
      <Form {...form}>
        <form onSubmit={submitForm}>
          <SettingsPageTitleStatusPortal>
            <span className='text-muted-foreground text-xs font-normal'>
              {t('CNY')}
            </span>
          </SettingsPageTitleStatusPortal>
          <SettingsPageActionsPortal>
            <Button
              type='button'
              size='sm'
              variant='outline'
              onClick={() => reset()}
              disabled={!isDirty || updateOption.isPending}
            >
              <HugeiconsIcon icon={UndoIcon} strokeWidth={2} />
              <span>{t('Reset')}</span>
            </Button>
            <Button
              type='submit'
              size='sm'
              disabled={!isDirty || updateOption.isPending}
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
                  'Set base prices by model and resolution. Seedance 2.0 Mini is billed per generation; other models are billed per million video tokens.'
                )}
              </CardDescription>
            </CardHeader>
            <CardContent>
              <FieldGroup>
                {[...modelGroups, SEEDANCE_MINI_MODEL].map((model) => {
                  const perRequest = model === SEEDANCE_MINI_MODEL
                  const priceRows = perRequest ? SEEDANCE_REQUEST_PRICE_ROWS : SEEDANCE_PRICE_ROWS
                  const rows = priceRows.map((row, index) => ({ ...row, index }))
                    .filter((row) => row.model === model)
                  const fieldPrefix = perRequest ? 'requests.rows' : 'rows'

                  return (
                    <FieldSet
                      key={model}
                      className='bg-muted/20 rounded-lg border p-3 sm:p-4'
                    >
                      <FieldLegend className='w-full'>
                        <span className='flex min-w-0 flex-col gap-0.5'>
                          <span>{t(rows[0].modelLabelKey)}</span>
                          <code className='text-muted-foreground text-xs font-normal break-all'>
                            {model}
                          </code>
                          <span className='text-muted-foreground text-xs font-normal'>
                            {perRequest ? t('CNY / request') : t('CNY / 1M video tokens')}
                          </span>
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
                                control={control}
                                name={`${fieldPrefix}.${row.index}.without_video`}
                                render={({ field, fieldState }) => {
                                  const inputId = `seedance-${model}-${row.index}-without-video`
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
                                control={control}
                                name={`${fieldPrefix}.${row.index}.with_video`}
                                render={({ field, fieldState }) => {
                                  const inputId = `seedance-${model}-${row.index}-with-video`
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
