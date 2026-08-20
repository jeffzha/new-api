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
import { Check, Copy } from 'lucide-react'
import { useEffect } from 'react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { useCopyToClipboard } from '@/hooks/use-copy-to-clipboard'
import { cn } from '@/lib/utils'

import { formatType } from '../lib/openapi-doc'
import type {
  DocEndpoint,
  DocField,
  DocModel,
  OpenApiParameter,
  OpenApiSchema,
  OpenApiSpec,
} from '../types'
import { DocsMarkdown } from './docs-markdown'
import { MethodBadge } from './method-badge'

interface FieldHint {
  tone: 'warning' | 'neutral'
  text: string
}

interface FieldRowProps {
  name: string
  type: string
  required: boolean
  description: string
  tone: 'neutral' | 'accent'
  hints?: FieldHint[]
  enumValues?: string[]
}

function FieldRow(props: FieldRowProps) {
  const { t } = useTranslation()
  return (
    <div className='grid gap-3 bg-white px-4 py-3 transition-colors hover:bg-slate-50/80 md:grid-cols-[minmax(0,13rem)_minmax(0,1fr)] dark:bg-slate-950 dark:hover:bg-slate-900/60'>
      <div className='min-w-0 space-y-1'>
        <div className='flex min-w-0 flex-wrap items-center gap-2'>
          <code
            className={cn(
              'max-w-full truncate rounded-md px-2 py-1 font-mono text-[13px] font-semibold ring-1',
              props.tone === 'accent'
                ? 'bg-sky-50 text-sky-700 ring-sky-100 dark:bg-sky-500/10 dark:text-sky-300 dark:ring-sky-500/20'
                : 'bg-slate-100 text-slate-800 ring-slate-200 dark:bg-slate-900 dark:text-slate-100 dark:ring-slate-800'
            )}
          >
            {props.name}
          </code>
          <Badge
            variant={props.required ? 'destructive' : 'outline'}
            className='h-5 rounded-md'
          >
            {t(props.required ? 'Required' : 'Optional')}
          </Badge>
        </div>
        <span className='block truncate font-mono text-xs text-slate-500 dark:text-slate-400'>
          {props.type}
        </span>
      </div>
      <div className='min-w-0 space-y-1.5 rounded-md bg-slate-50/60 px-3 py-2 text-sm leading-6 text-slate-600 ring-1 ring-slate-100 dark:bg-slate-900/40 dark:text-slate-300 dark:ring-slate-800'>
        {props.description ? <p>{props.description}</p> : null}
        {props.hints?.map((hint) => (
          <p
            key={hint.text}
            className={cn(
              'rounded-md px-2 py-1 text-xs leading-5 ring-1',
              hint.tone === 'warning'
                ? 'bg-amber-50 text-amber-800 ring-amber-100 dark:bg-amber-500/10 dark:text-amber-200 dark:ring-amber-500/20'
                : 'bg-slate-50 font-mono text-slate-600 ring-slate-100 dark:bg-slate-900 dark:text-slate-300 dark:ring-slate-800'
            )}
          >
            {hint.text}
          </p>
        ))}
        {props.enumValues && props.enumValues.length > 0 ? (
          <div className='rounded-md bg-slate-50 px-2 py-1.5 ring-1 ring-slate-100 dark:bg-slate-900 dark:ring-slate-800'>
            <div className='mb-1 text-[11px] leading-4 font-medium text-slate-500 dark:text-slate-400'>
              {t('Possible values')}
            </div>
            <div className='flex flex-wrap gap-1'>
              {props.enumValues.map((value) => (
                <span
                  key={value}
                  className='max-w-full min-w-0 rounded-md bg-white px-1.5 py-0.5 font-mono text-[11px] leading-4 break-all text-sky-700 ring-1 ring-sky-100 dark:bg-slate-950 dark:text-sky-300 dark:ring-sky-500/20'
                >
                  {value}
                </span>
              ))}
            </div>
          </div>
        ) : null}
      </div>
    </div>
  )
}

interface SectionHeadingProps {
  title: string
  meta?: string
}

function SectionHeading({ title, meta }: SectionHeadingProps) {
  return (
    <div className='flex flex-wrap items-center justify-between gap-2 border-b border-slate-200 pb-2 dark:border-slate-800'>
      <h2 className='text-base font-semibold text-slate-950 dark:text-slate-50'>
        {title}
      </h2>
      {meta ? (
        <span className='rounded-md bg-slate-100 px-2 py-0.5 font-mono text-[11px] text-slate-500 dark:bg-slate-800 dark:text-slate-400'>
          {meta}
        </span>
      ) : null}
    </div>
  )
}

interface ParameterTableProps {
  title: string
  spec: OpenApiSpec
  parameters: OpenApiParameter[]
}

function ParameterTable({ title, spec, parameters }: ParameterTableProps) {
  const { t } = useTranslation()
  if (parameters.length === 0) return null
  return (
    <section className='space-y-3'>
      <SectionHeading
        title={title}
        meta={t('{{count}} parameters', { count: parameters.length })}
      />
      <div className='overflow-hidden rounded-lg border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-950'>
        <div className='divide-y divide-slate-100 dark:divide-slate-800'>
          {parameters.map((parameter) => (
            <FieldRow
              key={`${parameter.in}-${parameter.name}`}
              name={parameter.name}
              type={`${parameter.in} · ${formatType(spec, parameter.schema)}`}
              required={parameter.required ?? false}
              description={parameter.description ?? ''}
              tone='neutral'
              hints={
                parameter.deprecated
                  ? [{ tone: 'warning', text: t('Deprecated') }]
                  : []
              }
            />
          ))}
        </div>
      </div>
    </section>
  )
}

interface SchemaTableProps {
  title: string
  contentType: string
  schema?: OpenApiSchema
  schemaDescription: string
  fields: DocField[]
  required?: boolean
}

function SchemaTable(props: SchemaTableProps) {
  const { t } = useTranslation()
  const schemaRef = props.schema?.$ref
    ? props.schema.$ref.replace('#/components/schemas/', '')
    : ''
  const emptyText =
    props.schemaDescription ||
    t(
      props.schema
        ? 'Schema is available as a raw object or array.'
        : 'No schema documented.'
    )

  return (
    <section className='space-y-3'>
      <SectionHeading title={props.title} meta={props.contentType} />
      <div className='overflow-hidden rounded-lg border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-950'>
        <div className='flex flex-wrap items-center gap-2 border-b border-slate-200 bg-slate-50 px-4 py-2 text-sm dark:border-slate-800 dark:bg-slate-900'>
          {schemaRef ? (
            <span className='rounded-md bg-white px-1.5 py-0.5 font-mono text-[11px] text-slate-500 ring-1 ring-slate-200 dark:bg-slate-950 dark:text-slate-400 dark:ring-slate-800'>
              {schemaRef}
            </span>
          ) : null}
          {props.required !== undefined ? (
            <Badge
              variant={props.required ? 'destructive' : 'outline'}
              className='h-5 rounded-md'
            >
              {t(props.required ? 'Required' : 'Optional')}
            </Badge>
          ) : null}
        </div>
        {props.fields.length > 0 ? (
          <div className='divide-y divide-slate-100 bg-slate-50/30 dark:divide-slate-800 dark:bg-slate-900/20'>
            {props.fields.map((field) => (
              <FieldRow
                key={field.name}
                name={field.name}
                type={field.type}
                required={field.required}
                description={field.description}
                tone='accent'
                enumValues={field.enumValues}
              />
            ))}
          </div>
        ) : (
          <div className='px-4 py-6 text-sm text-slate-500 dark:text-slate-400'>
            {emptyText}
          </div>
        )}
      </div>
    </section>
  )
}

function EndpointSummary({ endpoint }: { endpoint: DocEndpoint }) {
  const { t } = useTranslation()
  const { copiedText, copyToClipboard } = useCopyToClipboard({ notify: true })
  const copyValue = `${endpoint.method} ${endpoint.path}`
  const copied = copiedText === copyValue

  return (
    <section className='space-y-4'>
      <div className='space-y-2'>
        <div className='flex flex-wrap items-center gap-2 text-sm'>
          <span className='font-medium text-sky-700 dark:text-sky-300'>
            {endpoint.tag}
          </span>
          {endpoint.deprecated ? (
            <Badge variant='destructive'>{t('Deprecated')}</Badge>
          ) : null}
          {endpoint.operationId ? (
            <Badge variant='outline' className='rounded-md font-mono'>
              {endpoint.operationId}
            </Badge>
          ) : null}
        </div>
        <h1
          id='docs-endpoint-heading'
          tabIndex={-1}
          className='text-[28px] leading-tight font-semibold text-slate-950 outline-none dark:text-slate-50'
        >
          {endpoint.summary}
        </h1>
      </div>
      <div className='overflow-hidden rounded-lg border border-slate-200 bg-white shadow-[0_1px_2px_rgba(15,23,42,0.04)] dark:border-slate-800 dark:bg-slate-950'>
        <div className='flex min-w-0 flex-col gap-2 p-2 sm:flex-row sm:items-center'>
          <div className='flex min-w-0 flex-1 items-center gap-2 rounded-md bg-slate-50 px-2 py-1.5 dark:bg-slate-900'>
            <MethodBadge method={endpoint.method} />
            <code className='min-w-0 flex-1 truncate font-mono text-[13px] text-slate-800 dark:text-slate-100'>
              {endpoint.path}
            </code>
          </div>
          <Button
            type='button'
            size='sm'
            className='h-8 rounded-md bg-slate-900 px-2.5 text-xs font-semibold text-white hover:bg-slate-800 dark:bg-slate-100 dark:text-slate-950 dark:hover:bg-white'
            onClick={() => copyToClipboard(copyValue)}
          >
            {copied ? (
              <Check className='size-3.5 text-emerald-300 dark:text-emerald-600' />
            ) : (
              <Copy className='size-3.5' />
            )}
            {t('Copy endpoint')}
          </Button>
        </div>
      </div>
    </section>
  )
}

function Description({ description }: { description: string }) {
  const { t } = useTranslation()
  return (
    <section className='space-y-3'>
      <SectionHeading title={t('Description')} />
      {description ? <DocsMarkdown>{description}</DocsMarkdown> : null}
    </section>
  )
}

interface EndpointDetailProps {
  doc: DocModel
  endpoint: DocEndpoint
}

export function EndpointDetail({ doc, endpoint }: EndpointDetailProps) {
  const { t } = useTranslation()
  const spec = doc.source.spec
  const groupDescription =
    doc.groups.find((group) => group.title === endpoint.tag)?.description ?? ''
  const pathParameters = endpoint.parameters.filter((p) => p.in === 'path')
  const queryParameters = endpoint.parameters.filter((p) => p.in === 'query')
  const headerParameters = endpoint.parameters.filter((p) => p.in === 'header')

  useEffect(() => {
    const heading = document.querySelector<HTMLElement>(
      '#docs-endpoint-heading'
    )
    heading?.focus({ preventScroll: true })
  }, [endpoint.id])

  return (
    <article className='min-w-0 bg-white px-5 py-6 md:px-8 dark:bg-slate-950'>
      <div className='mx-auto max-w-4xl space-y-7'>
        <EndpointSummary endpoint={endpoint} />
        <Description
          description={[groupDescription, endpoint.description]
            .filter(Boolean)
            .join('\n\n')}
        />
        <ParameterTable
          title={t('Path parameters')}
          spec={spec}
          parameters={pathParameters}
        />
        <ParameterTable
          title={t('Query parameters')}
          spec={spec}
          parameters={queryParameters}
        />
        <ParameterTable
          title={t('Header parameters')}
          spec={spec}
          parameters={headerParameters}
        />
        <SchemaTable
          title={t('Request body')}
          contentType={endpoint.requestContentType}
          schema={endpoint.requestSchema}
          schemaDescription={endpoint.requestSchemaDescription}
          fields={endpoint.requestFields}
          required={endpoint.requestRequired}
        />
        <SchemaTable
          title={t('Response body')}
          contentType={endpoint.responseContentType}
          schema={endpoint.responseSchema}
          schemaDescription={endpoint.responseSchemaDescription}
          fields={endpoint.responseFields}
        />
        <ParameterTable
          title={t('Response headers')}
          spec={spec}
          parameters={endpoint.responseHeaders}
        />
      </div>
    </article>
  )
}
