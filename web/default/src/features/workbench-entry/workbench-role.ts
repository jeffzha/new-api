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
export const WORKBENCH_CUSTOMER_ROLES = [
  'owner',
  'admin',
  'member',
  'viewer',
] as const

export type WorkbenchCustomerRole = (typeof WORKBENCH_CUSTOMER_ROLES)[number]

const ROLE_LABEL_KEYS: Record<WorkbenchCustomerRole, string> = {
  owner: 'Customer owner',
  admin: 'Customer administrator',
  member: 'Customer member',
  viewer: 'Customer viewer',
}

export function isWorkbenchCustomerRole(
  role: string
): role is WorkbenchCustomerRole {
  return WORKBENCH_CUSTOMER_ROLES.some((candidate) => candidate === role)
}

export function workbenchRoleLabelKey(role: string): string {
  if (!isWorkbenchCustomerRole(role)) return 'Unsupported customer role'
  return ROLE_LABEL_KEYS[role]
}

export function selectableWorkbenchSelectionToken(
  role: string,
  selectionToken: string
): string | undefined {
  return isWorkbenchCustomerRole(role) ? selectionToken : undefined
}
