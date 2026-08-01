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
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import relayOpenApiSpec from '../../../../../docs/openapi/relay.json'
import { DocsHeader } from './components/docs-header'
import { DocsSidebar } from './components/docs-sidebar'
import { EndpointAside } from './components/endpoint-aside'
import { EndpointDetail } from './components/endpoint-detail'
import { buildDocModel } from './lib/openapi-doc'
import mobileOfficialSpec from './openapi-mobile-official-spec.json'
import openApiSpec from './openapi-spec.json'
import type { DocSource, OpenApiSpec } from './types'

const legacySpec = openApiSpec as unknown as OpenApiSpec
const mobileSpec = mobileOfficialSpec as unknown as OpenApiSpec
const relaySpec = relayOpenApiSpec as unknown as OpenApiSpec
const mobilePathPrefix = '/api/openapi-maas/exp/aicc/v2/'
const openAiTextPaths = [
  '/v1/chat/completions',
  '/v1/responses',
  '/v1/responses/compact',
] as const
const openAiTextTags = new Set(['OpenAI格式(Chat)', 'OpenAI格式(Responses)'])
const openAiTextSchemaNames = [
  'ChatCompletionRequest',
  'ChatCompletionResponse',
  'ErrorResponse',
  'Message',
  'MessageContent',
  'ResponseFormat',
  'ResponsesCompactionRequest',
  'ResponsesCompactionResponse',
  'ResponsesRequest',
  'ResponsesResponse',
  'Tool',
  'ToolCall',
  'Usage',
] as const
const replacedVideoPaths = new Set([
  '/v1/video/generations',
  '/v1/video/generations/{task_id}',
])
const legacyComponents = (
  legacySpec as unknown as { components?: Record<string, unknown> }
).components
const mobileComponents = (
  mobileSpec as unknown as { components?: Record<string, unknown> }
).components
const relayComponents = (
  relaySpec as unknown as { components?: Record<string, unknown> }
).components
const openAiTextSchemas = Object.fromEntries(
  openAiTextSchemaNames.flatMap((name) => {
    const schema = relaySpec.components?.schemas?.[name]
    return schema ? [[name, schema]] : []
  })
)
const mergedSpec = {
  ...legacySpec,
  info: {
    ...legacySpec.info,
    version: '2026-08-01.1',
    description:
      'Nexus Reach 对外 API 文档。已补充 OpenAI Chat Completions 与 Responses API；移动官方 Seedance 2.0 视频生成、素材库管理和真人认证接口保持原有分组和契约。',
  },
  servers: [],
  tags: [
    ...(relaySpec.tags ?? []).filter((tag) => openAiTextTags.has(tag.name)),
    ...(legacySpec.tags ?? []).filter(
      (tag) =>
        !openAiTextTags.has(tag.name) &&
        tag.name !== '移动视频-素材库' &&
        tag.name !== '移动视频-真人认证'
    ),
    ...(mobileSpec.tags ?? []),
  ],
  paths: {
    ...Object.fromEntries(
      Object.entries(legacySpec.paths).filter(
        ([path]) =>
          !path.startsWith(mobilePathPrefix) && !replacedVideoPaths.has(path)
      )
    ),
    ...Object.fromEntries(
      openAiTextPaths.flatMap((path) => {
        const pathItem = relaySpec.paths[path]
        return pathItem ? [[path, pathItem]] : []
      })
    ),
    ...mobileSpec.paths,
  },
  components: {
    ...legacyComponents,
    ...mobileComponents,
    securitySchemes: {
      ...(legacyComponents?.securitySchemes as
        | Record<string, unknown>
        | undefined),
      ...(relayComponents?.securitySchemes as
        | Record<string, unknown>
        | undefined),
      ...(mobileComponents?.securitySchemes as
        | Record<string, unknown>
        | undefined),
    },
    parameters: {
      ...(legacyComponents?.parameters as Record<string, unknown> | undefined),
      ...(mobileComponents?.parameters as Record<string, unknown> | undefined),
    },
    responses: {
      ...(mobileComponents?.responses as Record<string, unknown> | undefined),
      ...(legacyComponents?.responses as Record<string, unknown> | undefined),
    },
    schemas: {
      ...legacySpec.components?.schemas,
      ...openAiTextSchemas,
      ...mobileSpec.components?.schemas,
    },
  },
} as unknown as OpenApiSpec

const DOC_SOURCE: DocSource = {
  id: 'seedance-domestic',
  title: 'Seedance 2.0 API',
  subtitle: 'Seedance 2.0 domestic video and asset endpoints',
  spec: mergedSpec,
}

function pickDefaultEndpointId(doc: ReturnType<typeof buildDocModel>): string {
  const endpoints = doc.endpoints
  return (
    endpoints.find((endpoint) => endpoint.path.includes('/chat/completions'))
      ?.id ??
    endpoints.find((endpoint) => endpoint.method === 'POST')?.id ??
    endpoints[0]?.id ??
    ''
  )
}

/**
 * The backend-independent docs UI (bundled spec, no network). Rendered inside
 * {@link ApiDocs} for the in-app `/docs` route and reused by the standalone
 * static build.
 */
export function DocsContent() {
  const { t } = useTranslation()
  const [searchText, setSearchText] = useState('')

  const doc = useMemo(() => {
    const runtimeOrigin =
      typeof window !== 'undefined' && window.location.origin !== 'null'
        ? window.location.origin
        : ''
    const baseUrl = runtimeOrigin.replace(/\/$/, '')
    return buildDocModel(DOC_SOURCE, baseUrl)
  }, [])

  const [activeEndpointId, setActiveEndpointId] = useState(() =>
    pickDefaultEndpointId(doc)
  )

  const filteredGroups = useMemo(() => {
    const query = searchText.trim().toLowerCase()
    if (!query) return doc.groups
    return doc.groups
      .map((group) => ({
        ...group,
        endpoints: group.endpoints.filter((endpoint) =>
          endpoint.searchText.includes(query)
        ),
      }))
      .filter((group) => group.endpoints.length > 0)
  }, [doc.groups, searchText])

  const allGroupIds = useMemo(
    () => doc.groups.map((group) => group.id),
    [doc.groups]
  )

  const visibleEndpoints = useMemo(
    () => filteredGroups.flatMap((group) => group.endpoints),
    [filteredGroups]
  )

  const activeEndpoint =
    visibleEndpoints.find((endpoint) => endpoint.id === activeEndpointId) ??
    visibleEndpoints[0] ??
    doc.endpoints.find((endpoint) => endpoint.id === activeEndpointId) ??
    doc.endpoints[0]

  return (
    <main className='min-h-svh bg-white pt-16 text-slate-900 dark:bg-slate-950 dark:text-slate-100'>
      <DocsHeader doc={doc} />
      <div className='grid min-h-[calc(100svh-8rem)] grid-cols-1 lg:grid-cols-[19rem_minmax(0,1fr)] xl:grid-cols-[19rem_minmax(0,1fr)_24rem]'>
        <DocsSidebar
          groups={filteredGroups}
          allGroupIds={allGroupIds}
          activeEndpointId={activeEndpoint?.id ?? ''}
          searchText={searchText}
          onSearchTextChange={setSearchText}
          onEndpointSelect={setActiveEndpointId}
        />
        {activeEndpoint ? (
          <>
            <EndpointDetail doc={doc} endpoint={activeEndpoint} />
            <EndpointAside endpoint={activeEndpoint} />
          </>
        ) : (
          <div className='px-5 py-10 text-sm text-slate-500 md:px-8'>
            {t('No documentation sections found.')}
          </div>
        )}
      </div>
    </main>
  )
}
