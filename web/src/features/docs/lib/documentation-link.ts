/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
/** Resolve only the administrator-configured target, never a caller's query string. */
export function resolveDocumentationLink(
  configured: unknown,
  origin: string
): string | null {
  if (typeof configured !== 'string' || !configured.trim()) return null
  const value = configured.trim()
  // Reject URL parser normalization of control characters and backslashes.
  // oxlint-disable-next-line no-control-regex
  if (/[\u0000-\u001f\u007f\\]/.test(value) || value.startsWith('//')) {
    return null
  }
  if (!value.startsWith('/') && !/^https?:\/\//i.test(value)) return null
  try {
    const url = new URL(value, origin)
    if (
      !['http:', 'https:'].includes(url.protocol) ||
      url.username ||
      url.password
    ) {
      return null
    }
    if (url.origin === new URL(origin).origin) {
      if (url.pathname.replace(/\/+$/, '') === '/docs') return null
      return `${url.pathname}${url.search}${url.hash}`
    }
    return url.href
  } catch {
    return null
  }
}
