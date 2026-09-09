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
/**
 * Minimal OpenAPI 3.0.x typings — only the subset consumed by the docs viewer.
 * The spec is authored/exported elsewhere, so unknown members stay permissive.
 */
export interface OpenApiSchema {
  type?: string
  format?: string
  nullable?: boolean
  description?: string
  properties?: Record<string, OpenApiSchema>
  items?: OpenApiSchema
  required?: string[]
  enum?: unknown[]
  allOf?: OpenApiSchema[]
  oneOf?: OpenApiSchema[]
  anyOf?: OpenApiSchema[]
  example?: unknown
  default?: unknown
  $ref?: string
}

export interface OpenApiMediaType {
  schema?: OpenApiSchema
  example?: unknown
  examples?: Record<string, { value?: unknown }>
}

export interface OpenApiParameter {
  name: string
  in: 'query' | 'path' | 'header' | 'cookie'
  description?: string
  required?: boolean
  deprecated?: boolean
  example?: unknown
  schema?: OpenApiSchema
}

export interface OpenApiHeader {
  description?: string
  required?: boolean
  deprecated?: boolean
  example?: unknown
  schema?: OpenApiSchema
}

export interface OpenApiOperation {
  tags?: string[]
  summary?: string
  description?: string
  operationId?: string
  deprecated?: boolean
  parameters?: OpenApiParameter[]
  requestBody?: {
    required?: boolean
    content?: Record<string, OpenApiMediaType>
  }
  responses?: Record<
    string,
    {
      description?: string
      headers?: Record<string, OpenApiHeader>
      content?: Record<string, OpenApiMediaType>
    }
  >
}

export type OpenApiPathItem = Partial<Record<HttpMethod, OpenApiOperation>>

export interface OpenApiSpec {
  openapi: string
  info: { title: string; description?: string; version: string }
  tags?: { name: string; description?: string }[]
  paths: Record<string, OpenApiPathItem>
  components?: { schemas?: Record<string, OpenApiSchema> }
  servers?: { url: string }[]
}

export type HttpMethod =
  | 'get'
  | 'post'
  | 'put'
  | 'patch'
  | 'delete'
  | 'head'
  | 'options'

/** The static document a viewer renders — a titled wrapper around one spec. */
export interface DocSource {
  id: string
  title: string
  subtitle: string
  spec: OpenApiSpec
}

/** A single property extracted from a request/response schema. */
export interface DocField {
  name: string
  type: string
  required: boolean
  description: string
  enumValues: string[]
}

/** A copy-ready code sample shown in the right-hand panel. */
export interface DocExample {
  id: string
  label: string
  code: string
}

/** A fully flattened endpoint ready to render. */
export interface DocEndpoint {
  id: string
  method: string
  path: string
  summary: string
  description: string
  operationId: string
  deprecated: boolean
  tag: string
  parameters: OpenApiParameter[]
  requestContentType: string
  requestRequired: boolean
  requestSchema?: OpenApiSchema
  requestSchemaDescription: string
  requestFields: DocField[]
  requestExample: unknown
  responseContentType: string
  responseStatusCodes: string[]
  responseHeaders: OpenApiParameter[]
  responseSchema?: OpenApiSchema
  responseSchemaDescription: string
  responseFields: DocField[]
  requestExamples: DocExample[]
  responseExamples: DocExample[]
  curlExample: DocExample
  searchText: string
}

/** Endpoints grouped by their primary tag. */
export interface DocGroup {
  id: string
  title: string
  description: string
  endpoints: DocEndpoint[]
}

/** The complete view model produced from a {@link DocSource}. */
export interface DocModel {
  source: DocSource
  groups: DocGroup[]
  endpoints: DocEndpoint[]
  endpointCount: number
  methodCounts: Record<string, number>
}
