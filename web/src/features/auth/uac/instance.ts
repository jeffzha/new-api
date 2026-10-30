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
// A project build can be served below /test without changing product API URLs.
const configuredBase = import.meta.env.VITE_INSTANCE_BASE_PATH ?? ''
export const instanceBase = configuredBase.endsWith('/')
  ? configuredBase.slice(0, -1)
  : configuredBase
export const instanceNamespace = instanceBase ? `new-api:${instanceBase}:` : ''

export function instanceAsset(path: string): string {
  if (!instanceBase || !path.startsWith('/') || path.startsWith('//')) {
    return path
  }
  if (path === instanceBase || path.startsWith(`${instanceBase}/`)) return path
  return `${instanceBase}${path}`
}

export function instancePath(path: string): string {
  return `${instanceBase}${path}`
}
