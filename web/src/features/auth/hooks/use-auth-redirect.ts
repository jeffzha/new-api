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
import { useNavigate } from '@tanstack/react-router'
import i18n from 'i18next'
import { useCallback } from 'react'

import {
  getSavedLanguage,
  sanitizeAuthRedirect,
} from '@/features/auth/lib/auth-redirect'
import { applyAuthBundle, isAuthBundle } from '@/lib/api'
import { AuthOperationError } from '@/lib/secure-verification'
import { useAuthStore, type AuthBundle } from '@/stores/auth-store'

import { isLoginChallenge } from '../secure-verification/api'

/**
 * Hook for handling authentication redirects and user data management
 */
export function useAuthRedirect() {
  const navigate = useNavigate()

  /**
   * Handle successful login
   * @param userData - Optional user data from login response
   * @param redirectTo - Redirect path after login
   */
  const handleLoginSuccess = useCallback(
    async (bundle: AuthBundle, redirectTo?: string) => {
      applyAuthBundle(bundle)
      const savedLang = getSavedLanguage(bundle.user)
      if (savedLang && savedLang !== i18n.language) {
        await i18n.changeLanguage(savedLang)
      }

      const targetPath =
        sanitizeAuthRedirect(redirectTo, window.location.origin) ?? '/dashboard'
      await navigate({ href: targetPath, replace: true })
    },
    [navigate]
  )

  const handleLoginResult = useCallback(
    async (result: unknown, redirectTo?: string): Promise<boolean> => {
      if (isAuthBundle(result)) {
        await handleLoginSuccess(result, redirectTo)
        return true
      }
      if (!isLoginChallenge(result)) {
        throw new AuthOperationError('Login failed')
      }
      if (result.expires_at * 1000 <= Date.now()) {
        throw new AuthOperationError('Login flow expired. Please sign in again.')
      }
      useAuthStore.getState().auth.setPendingLoginVerification({
        challenge: result,
        redirectTo:
          sanitizeAuthRedirect(redirectTo, window.location.origin) ?? undefined,
      })
      await navigate({ to: '/otp', replace: true })
      return false
    },
    [handleLoginSuccess, navigate]
  )

  /**
   * Redirect to login page
   */
  const redirectToLogin = (redirectTo?: string) => {
    navigate({
      to: '/sign-in',
      search: redirectTo ? { redirect: redirectTo } : {},
      replace: true,
    })
  }

  /**
   * Redirect to register page
   */
  const redirectToRegister = () => {
    navigate({ to: '/sign-up', replace: true })
  }

  return {
    handleLoginSuccess,
    handleLoginResult,
    redirectToLogin,
    redirectToRegister,
  }
}
