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
import { createFileRoute, redirect } from '@tanstack/react-router'
import { z } from 'zod'

import { sanitizeAuthRedirect } from '@/features/auth/lib/auth-redirect'
import { SignIn } from '@/features/auth/sign-in'
import {
  getAgencyCenterUrl,
  shouldAutoEnterAgencyCenter,
} from '@/lib/agency-center'
import { resolveAuthentication } from '@/lib/auth-session'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

const searchSchema = z.object({
  redirect: z.string().optional(),
  mode: z.enum(['agency']).optional(),
  agency_signed_out: z.boolean().optional(),
})

export const Route = createFileRoute('/(auth)/sign-in')({
  component: SignIn,
  validateSearch: searchSchema,
  beforeLoad: async ({ search }) => {
    // 根 guard 可能因为没有会话提示而跳过了 refresh。此处必须回源确认，
    // 否则持有有效 Refresh Cookie 的用户会被要求重新输入密码。
    await resolveAuthentication()

    const { auth } = useAuthStore.getState()

    // 代理商运营账号使用独立会话。只有主站超级管理员可以直接 SSO，
    // 其他已登录主站用户以及刚主动退出代理商中心的管理员，仍停留在
    // 统一页面输入代理商账号，避免退出后被主站会话立即自动登录回来。
    if (auth.user && search?.mode === 'agency') {
      if (
        shouldAutoEnterAgencyCenter(
          auth.user.role === ROLE.SUPER_ADMIN,
          search.mode,
          search.agency_signed_out
        )
      ) {
        throw redirect({ href: getAgencyCenterUrl(true), replace: true })
      }
      return
    }

    if (auth.user) {
      const target =
        sanitizeAuthRedirect(search?.redirect, window.location.origin) ??
        '/dashboard'
      throw redirect({ href: target, replace: true })
    }
  },
})
