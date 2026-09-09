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
import { useEffect, useMemo, useRef } from 'react'
import { useForm, useWatch } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import * as z from 'zod'

import { JsonCodeEditor } from '@/components/json-code-editor'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'

import {
  SettingsForm,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useUpdateOption } from '../hooks/use-update-option'
import { formatJsonForTextarea, normalizeJsonString } from './utils'

type RegistryVersion = {
  id: string
}

type RegistryDocument = {
  schema_version: number
  enabled: boolean
  active_version: string
  versions: RegistryVersion[]
  [key: string]: unknown
}

const parseRegistry = (value: string): RegistryDocument | null => {
  try {
    const parsed = JSON.parse(value) as Partial<RegistryDocument>
    if (
      parsed.schema_version !== 1 ||
      typeof parsed.enabled !== 'boolean' ||
      typeof parsed.active_version !== 'string' ||
      !Array.isArray(parsed.versions) ||
      parsed.versions.some(
        (version) => !version || typeof version.id !== 'string'
      )
    ) {
      return null
    }
    if (
      !parsed.versions.some((version) => version.id === parsed.active_version)
    ) {
      return null
    }
    return parsed as RegistryDocument
  } catch {
    return null
  }
}

const schema = z.object({
  registry: z.string().superRefine((value, ctx) => {
    if (!parseRegistry(value)) {
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        message: 'Invalid JSON',
      })
    }
  }),
})

type FormInput = z.input<typeof schema>
type FormValues = z.output<typeof schema>

type Props = {
  defaultValue: string
}

export function ModelCompatibilitySettingsCard({ defaultValue }: Props) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()
  const normalizedDefault = useMemo(
    () => normalizeJsonString(defaultValue),
    [defaultValue]
  )
  const baselineRef = useRef(normalizedDefault)
  const form = useForm<FormInput, unknown, FormValues>({
    resolver: zodResolver(schema),
    defaultValues: { registry: formatJsonForTextarea(defaultValue) },
  })
  const registryValue = useWatch({ control: form.control, name: 'registry' })
  const registry = useMemo(
    () => parseRegistry(registryValue ?? ''),
    [registryValue]
  )

  useEffect(() => {
    baselineRef.current = normalizedDefault
    form.reset({ registry: formatJsonForTextarea(defaultValue) })
  }, [defaultValue, form, normalizedDefault])

  const updateRegistryDocument = (
    mutate: (document: RegistryDocument) => void
  ) => {
    if (!registry) return
    const next = structuredClone(registry)
    mutate(next)
    form.setValue('registry', JSON.stringify(next, null, 2), {
      shouldDirty: true,
      shouldValidate: true,
    })
  }

  const onSubmit = async (values: FormValues) => {
    const normalized = normalizeJsonString(values.registry)
    if (normalized === baselineRef.current) {
      toast.info(t('No changes to save'))
      return
    }
    await updateOption.mutateAsync({
      key: 'model_compatibility.registry',
      value: normalized,
    })
    baselineRef.current = normalized
    form.reset({ registry: formatJsonForTextarea(normalized) })
  }

  return (
    <SettingsSection title={t('Model Compatibility Registry')}>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)}>
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            isSaving={updateOption.isPending}
          />

          <SettingsSwitchItem>
            <SettingsSwitchContent>
              <Label htmlFor='model-compatibility-enabled'>
                {t('Enable Compatibility Rules')}
              </Label>
              <p className='text-muted-foreground text-sm'>
                {t(
                  'Normalize documented model parameter differences before channel parameter overrides are applied.'
                )}
              </p>
            </SettingsSwitchContent>
            <Switch
              id='model-compatibility-enabled'
              checked={registry?.enabled ?? false}
              disabled={!registry}
              onCheckedChange={(enabled) =>
                updateRegistryDocument((document) => {
                  document.enabled = enabled
                })
              }
            />
          </SettingsSwitchItem>

          <div className='grid max-w-md gap-2'>
            <Label htmlFor='model-compatibility-active-version'>
              {t('Active Registry Version')}
            </Label>
            <Select
              value={registry?.active_version ?? ''}
              disabled={!registry}
              onValueChange={(version) => {
                if (!version) return
                updateRegistryDocument((document) => {
                  document.active_version = version
                })
              }}
            >
              <SelectTrigger id='model-compatibility-active-version'>
                <SelectValue placeholder={t('Select a version')} />
              </SelectTrigger>
              <SelectContent>
                {registry?.versions.map((version) => (
                  <SelectItem key={version.id} value={version.id}>
                    {version.id}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <p className='text-muted-foreground text-sm'>
              {t(
                'Switching the active version is atomic. Select the previous version to roll back immediately.'
              )}
            </p>
          </div>

          <FormField
            control={form.control}
            name='registry'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Registry JSON')}</FormLabel>
                <FormControl>
                  <JsonCodeEditor
                    value={field.value}
                    onChange={field.onChange}
                    name={field.name}
                    onBlur={field.onBlur}
                    textareaRef={field.ref}
                    aria-invalid={Boolean(form.formState.errors.registry)}
                  />
                </FormControl>
                <FormDescription>
                  {t(
                    'Create a new version before changing production rules. Profiles can target channel types, channel IDs, groups, models, and API formats.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}
