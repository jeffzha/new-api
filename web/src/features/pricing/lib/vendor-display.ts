/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
// Keep the original vendor name as the filter identity. Only known brand aliases
// are localized; administrator-defined names remain unchanged.
export function vendorDisplayName(name: string, language: string): string {
  if (
    !['字节跳动', '字節跳動', 'bytedance'].includes(name.trim().toLowerCase())
  ) {
    return name
  }
  if (
    language.toLowerCase().startsWith('zh-tw') ||
    language.toLowerCase().startsWith('zh-hk')
  ) {
    return '字節跳動'
  }
  if (language.toLowerCase().startsWith('zh')) return '字节跳动'
  return 'ByteDance'
}
