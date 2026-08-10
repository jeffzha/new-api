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
export function requireLocalSSORedirect(
  value: string | undefined,
  origin: string
): string {
  if (value == null || value.trim() === '') {
    throw new Error('Missing workbench SSO redirect')
  }
  const target = new URL(value, origin)
  if (
    target.origin !== origin ||
    target.pathname !== '/workbench/auth/sso' ||
    target.hash !== '' ||
    target.searchParams.size !== 1 ||
    target.searchParams.get('ticket')?.trim() === '' ||
    target.searchParams.get('ticket') == null
  ) {
    throw new Error('Invalid workbench SSO redirect')
  }
  return target.toString()
}
