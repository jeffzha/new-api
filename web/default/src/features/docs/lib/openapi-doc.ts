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
import type {
  DocEndpoint,
  DocExample,
  DocField,
  DocGroup,
  DocModel,
  DocSource,
  OpenApiMediaType,
  OpenApiOperation,
  OpenApiParameter,
  OpenApiSchema,
  OpenApiSpec,
} from '../types'

/** HTTP methods rendered by the viewer, in canonical display order. */
export const HTTP_METHODS = [
  'get',
  'post',
  'put',
  'patch',
  'delete',
  'head',
  'options',
] as const

/** Tags (and tag prefixes) that should never surface in the docs. */
const EXCLUDED_TAG_PREFIXES = ['未实现']

/** Resolve a `$ref` and collapse the first `allOf`/`oneOf`/`anyOf` branch. */
export function resolveSchema(
  spec: OpenApiSpec,
  schema?: OpenApiSchema
): OpenApiSchema {
  const dereferenced = dereference(spec, schema)
  const composed =
    dereferenced.allOf?.[0] ??
    dereferenced.oneOf?.[0] ??
    dereferenced.anyOf?.[0]
  if (!composed) return dereferenced
  return {
    ...dereferenced,
    ...resolveSchema(spec, composed),
    description: dereferenced.description ?? composed.description,
  }
}

function dereference(spec: OpenApiSpec, schema?: OpenApiSchema): OpenApiSchema {
  if (!schema?.$ref) return schema ?? {}
  const name = schema.$ref.replace('#/components/schemas/', '')
  return spec.components?.schemas?.[name] ?? schema
}

/** The effective JSON type of a schema, inferring `object`/`array` from shape. */
function effectiveType(schema: OpenApiSchema): string {
  if (schema.type) return schema.type
  if (schema.properties) return 'object'
  if (schema.items) return 'array'
  return 'unknown'
}

/** Human-readable type label, e.g. `string<date-time> | null`, `object[]`. */
export function formatType(spec: OpenApiSpec, schema?: OpenApiSchema): string {
  const resolved = resolveSchema(spec, schema)
  const type = effectiveType(resolved)
  const format = resolved.format ? `<${resolved.format}>` : ''
  const nullable = resolved.nullable ? ' | null' : ''
  if (type === 'array') {
    return `${formatType(spec, resolved.items)}[]${nullable}`
  }
  return `${type}${format}${nullable}`
}

/** Flatten the top-level properties of a schema (or of an array's item). */
export function extractFields(
  spec: OpenApiSpec,
  schema?: OpenApiSchema
): DocField[] {
  const resolved = resolveSchema(spec, schema)
  const base =
    resolved.type === 'array' ? resolveSchema(spec, resolved.items) : resolved
  const properties = base.properties ?? {}
  const required = new Set(base.required ?? [])
  return Object.entries(properties).map(([name, propSchema]) => {
    const resolvedProp = resolveSchema(spec, propSchema)
    return {
      name,
      type: formatType(spec, propSchema),
      required: required.has(name),
      description: normalizeText(propSchema.description ?? ''),
      enumValues: resolvedProp.enum?.length
        ? resolvedProp.enum.map((value) => String(value))
        : [],
    }
  })
}

/** Collapse runaway blank lines so descriptions stay compact. */
export function normalizeText(text: string): string {
  return text.replaceAll(/\n{3,}/g, '\n\n').trim()
}

function firstContent(
  content?: Record<string, OpenApiMediaType>
): [string, OpenApiMediaType] {
  return Object.entries(content ?? {})[0] ?? ['application/json', {}]
}

function firstExample(media?: OpenApiMediaType): unknown {
  const values = Object.values(media?.examples ?? {})
  return values[0]?.value
}

function buildExample(
  id: string,
  label: string,
  value: unknown
): DocExample | undefined {
  if (value === undefined) return undefined
  return {
    id,
    label,
    code:
      typeof value === 'string' ? value : JSON.stringify(value, undefined, 2),
  }
}

function materializeBaseUrl(value: unknown, baseUrl: string): unknown {
  if (typeof value === 'string') {
    return value.replaceAll('{{BASE_URL}}', baseUrl)
  }
  if (Array.isArray(value)) {
    return value.map((item) => materializeBaseUrl(item, baseUrl))
  }
  if (value && typeof value === 'object') {
    return Object.fromEntries(
      Object.entries(value).map(([key, item]) => [
        key,
        materializeBaseUrl(item, baseUrl),
      ])
    )
  }
  return value
}

/** Sort tags by their declaration order in the spec, falling back to locale. */
function compareTags(spec: OpenApiSpec, a: string, b: string): number {
  const order = new Map(
    (spec.tags ?? []).map((tag, index) => [tag.name, index])
  )
  const ai = order.get(a) ?? Number.MAX_SAFE_INTEGER
  const bi = order.get(b) ?? Number.MAX_SAFE_INTEGER
  return ai === bi ? a.localeCompare(b) : ai - bi
}

function methodOrder(method: string): number {
  const index = HTTP_METHODS.indexOf(
    method.toLowerCase() as (typeof HTTP_METHODS)[number]
  )
  return index === -1 ? HTTP_METHODS.length : index
}

function isExcludedTag(tag: string): boolean {
  return EXCLUDED_TAG_PREFIXES.some(
    (prefix) => tag === prefix || tag.startsWith(`${prefix}/`)
  )
}

/** Deterministic short hash used to disambiguate generated endpoint ids. */
function hashString(input: string): string {
  let hash = 5381
  for (const char of input) {
    hash = (33 * hash) ^ (char.codePointAt(0) ?? 0)
  }
  return (hash >>> 0).toString(36)
}

function endpointId(method: string, path: string): string {
  const raw = `${method.toLowerCase()}-${path}`
  const slug = raw
    .replaceAll(/[^a-z0-9]+/gi, '-')
    .replaceAll(/^-+|-+$/g, '')
    .toLowerCase()
  if (slug && slug !== method.toLowerCase()) return slug
  return `${slug || 'id'}-${hashString(raw)}`
}

/**
 * Synthesize a representative example value for a schema when the spec ships
 * none. Heuristics mirror the field name so samples read naturally.
 */
function generateSample(
  spec: OpenApiSpec,
  schema: OpenApiSchema | undefined,
  key = 'value',
  baseUrl = '',
  seen = new Set<string>()
): unknown {
  if (!schema) return undefined
  if (schema.example !== undefined) return schema.example
  if (schema.default !== undefined) return schema.default
  if (schema.enum && schema.enum.length > 0) return schema.enum[0]
  if (schema.$ref) {
    if (seen.has(schema.$ref)) return {}
    seen.add(schema.$ref)
  }

  const resolved = resolveSchema(spec, schema)
  if (resolved.example !== undefined) return resolved.example
  if (resolved.default !== undefined) return resolved.default
  if (resolved.enum && resolved.enum.length > 0) return resolved.enum[0]

  const type = effectiveType(resolved)

  if (type === 'array') {
    const item = generateSample(spec, resolved.items, key, baseUrl, seen)
    return item === undefined ? [] : [item]
  }

  if (type === 'object' || resolved.properties) {
    const properties = resolved.properties ?? {}
    const value: Record<string, unknown> = {}
    for (const [name, propSchema] of Object.entries(properties)) {
      const sample = generateSample(spec, propSchema, name, baseUrl, seen)
      if (sample !== undefined) value[name] = sample
    }
    return value
  }

  if (type === 'integer' || type === 'number') {
    const lowered = key.toLowerCase()
    if (
      lowered.includes('created') ||
      lowered.includes('updated') ||
      lowered.includes('time')
    ) {
      return 1790250000
    }
    return lowered.includes('total') ? 1 : 0
  }

  if (type === 'boolean') return false

  if (type === 'string') {
    const lowered = key.toLowerCase()
    if (lowered.includes('url')) {
      return `${baseUrl}/apidocs/example-resource`
    }
    if (lowered.includes('model')) return 'gpt-4o'
    if (lowered === 'object') return 'object'
    if (lowered.includes('status')) return 'succeeded'
    if (lowered.includes('id')) return `${key}_xxx`
    if (resolved.format === 'date-time') return '2026-06-30T12:00:00Z'
    if (resolved.format === 'date') return '2026-06-30'
    if (resolved.format === 'uri' || resolved.format === 'url') {
      return `${baseUrl}/apidocs/example-resource`
    }
    return 'string'
  }

  return {}
}

function extractRequest(spec: OpenApiSpec, operation: OpenApiOperation) {
  const [contentType, media] = firstContent(operation.requestBody?.content)
  const schema = media.schema
  const resolved = resolveSchema(spec, schema)
  return {
    contentType,
    schema,
    schemaDescription: normalizeText(resolved.description ?? ''),
    fields: extractFields(spec, schema),
    example: media.example ?? firstExample(media),
    required: operation.requestBody?.required ?? false,
  }
}

function extractResponse(
  spec: OpenApiSpec,
  operation: OpenApiOperation,
  baseUrl: string
) {
  const responses = operation.responses ?? {}
  const statusCodes = Object.keys(responses)
  const successKey =
    statusCodes.find((code) => /^2\d\d$/.test(code)) ?? statusCodes[0] ?? '200'
  const response = responses[successKey]
  const [contentType, media] = firstContent(response?.content)
  const schema = media.schema
  const resolved = resolveSchema(spec, schema)
  const example = media.example ?? firstExample(media)
  const headers: OpenApiParameter[] = []
  const headerNames = new Set<string>()
  for (const [statusCode, responseItem] of Object.entries(responses)) {
    if (!/^2\d\d$/.test(statusCode)) continue
    for (const [name, header] of Object.entries(responseItem.headers ?? {})) {
      const normalizedName = name.toLowerCase()
      if (headerNames.has(normalizedName)) continue
      headerNames.add(normalizedName)
      headers.push({
        name,
        in: 'header' as const,
        description: header.description,
        required: header.required,
        deprecated: header.deprecated,
        example: header.example,
        schema: header.schema,
      })
    }
  }
  return {
    contentType,
    statusCodes,
    headers,
    schema,
    schemaDescription: normalizeText(resolved.description ?? ''),
    fields: extractFields(spec, schema),
    example: example ?? generateSample(spec, schema, 'response', baseUrl),
  }
}

function buildCurl(
  baseUrl: string,
  method: string,
  path: string,
  parameters: OpenApiOperation['parameters'],
  contentType: string,
  example: unknown
): DocExample {
  let examplePath = path
  for (const parameter of parameters ?? []) {
    if (parameter.in !== 'path') continue
    const parameterExample = parameter.example ?? parameter.schema?.example
    if (parameterExample === undefined) continue
    examplePath = examplePath.replaceAll(
      `{${parameter.name}}`,
      encodeURIComponent(String(parameterExample))
    )
  }

  const hasBody = example !== undefined
  const data =
    typeof example === 'string'
      ? example
      : JSON.stringify(example ?? {}, undefined, 2)
  const lines = [
    `curl --location -X ${method.toUpperCase()} '${baseUrl}${examplePath}'`,
    "  --header 'Authorization: Bearer sk-your-key'",
  ]
  for (const parameter of parameters ?? []) {
    if (parameter.in !== 'header') continue
    const parameterExample = parameter.example ?? parameter.schema?.example
    if (parameterExample === undefined) continue
    lines.push(`  --header '${parameter.name}: ${String(parameterExample)}'`)
  }
  if (hasBody) {
    lines.push(`  --header 'Content-Type: ${contentType}'`)
    lines.push(`  --data '${data.replaceAll("'", "'\\''")}'`)
  }
  return { id: 'curl', label: 'cURL', code: lines.join(' \\\n') }
}

function buildEndpoint(
  spec: OpenApiSpec,
  baseUrl: string,
  method: string,
  path: string,
  operation: OpenApiOperation
): DocEndpoint {
  const tag = operation.tags?.[0] ?? 'Default'
  const request = extractRequest(spec, operation)
  const response = extractResponse(spec, operation, baseUrl)
  const resolvedRequestExample = materializeBaseUrl(request.example, baseUrl)
  const resolvedResponseExample = materializeBaseUrl(response.example, baseUrl)
  const requestExample = buildExample(
    'request-json',
    'JSON',
    resolvedRequestExample
  )
  const responseExample = buildExample(
    'response-json',
    'Response JSON',
    resolvedResponseExample
  )
  const searchParts = [
    method,
    path,
    tag,
    operation.summary,
    operation.description,
    operation.operationId,
    ...(operation.parameters ?? []).map((param) => param.name),
    ...request.fields.map((field) => field.name),
    ...response.fields.map((field) => field.name),
    ...response.headers.map((header) => header.name),
  ]

  return {
    id: endpointId(method, path),
    method,
    path,
    summary: operation.summary || operation.operationId || path,
    description: normalizeText(operation.description ?? ''),
    operationId: operation.operationId ?? '',
    deprecated: operation.deprecated ?? false,
    tag,
    parameters: operation.parameters ?? [],
    requestContentType: request.contentType,
    requestRequired: request.required,
    requestSchema: request.schema,
    requestSchemaDescription: request.schemaDescription,
    requestFields: request.fields,
    requestExample: resolvedRequestExample,
    responseContentType: response.contentType,
    responseStatusCodes: response.statusCodes,
    responseHeaders: response.headers,
    responseSchema: response.schema,
    responseSchemaDescription: response.schemaDescription,
    responseFields: response.fields,
    requestExamples: requestExample ? [requestExample] : [],
    responseExamples: responseExample ? [responseExample] : [],
    curlExample: buildCurl(
      baseUrl,
      method,
      path,
      operation.parameters,
      request.contentType,
      resolvedRequestExample
    ),
    searchText: searchParts.filter(Boolean).join(' ').toLowerCase(),
  }
}

/**
 * Flatten an OpenAPI spec into the grouped, searchable view model the docs
 * page renders. `baseUrl` is baked into the generated cURL samples.
 */
export function buildDocModel(source: DocSource, baseUrl: string): DocModel {
  const spec = source.spec
  const endpoints = Object.entries(spec.paths ?? {})
    .flatMap(([path, item]) =>
      HTTP_METHODS.flatMap((method) => {
        const operation = item[method]
        return operation ? [{ method, path, operation }] : []
      })
    )
    .filter(({ operation }) => !isExcludedTag(operation.tags?.[0] ?? ''))
    .map(({ method, path, operation }) =>
      buildEndpoint(spec, baseUrl, method.toUpperCase(), path, operation)
    )
    .filter((endpoint) => !isExcludedTag(endpoint.tag))
    .sort(
      (a, b) =>
        compareTags(spec, a.tag, b.tag) ||
        a.path.localeCompare(b.path) ||
        methodOrder(a.method) - methodOrder(b.method)
    )

  const tagDescriptions = new Map(
    (spec.tags ?? []).map((tag) => [tag.name, tag.description ?? ''])
  )
  const groupsByTag = new Map<string, DocGroup>()
  const methodCounts: Record<string, number> = {}

  for (const endpoint of endpoints) {
    methodCounts[endpoint.method] = (methodCounts[endpoint.method] ?? 0) + 1
    const group = groupsByTag.get(endpoint.tag) ?? {
      id: `tag:${endpoint.tag}`,
      title: endpoint.tag,
      description: tagDescriptions.get(endpoint.tag) ?? '',
      endpoints: [],
    }
    group.endpoints.push(endpoint)
    groupsByTag.set(endpoint.tag, group)
  }

  const groups = [...groupsByTag.values()].sort((a, b) =>
    compareTags(spec, a.title, b.title)
  )

  return {
    source,
    groups,
    endpoints,
    endpointCount: endpoints.length,
    methodCounts,
  }
}
