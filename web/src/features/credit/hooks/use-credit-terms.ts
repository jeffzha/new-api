/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation } from '@tanstack/react-query'
import { useEffect } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { handleServerError } from '@/lib/handle-server-error'
import { requireServerSuccess } from '@/lib/server-error-message'

import { saveCreditAccount } from '../api'
import { creditLimitFromInput, creditLimitToInput } from '../lib/format'
import {
  creditTermsSchema,
  type CreditTermsFormValues,
} from '../lib/terms-schema'
import type { CreditAccount } from '../types'

export function useCreditTerms(
  account: CreditAccount | undefined,
  onSaved: (account: CreditAccount) => void
) {
  const { t } = useTranslation()
  const form = useForm<CreditTermsFormValues>({
    resolver: zodResolver(creditTermsSchema),
    defaultValues: {
      limit: '',
      termDays: '0',
      status: 'active',
      emailEnabled: false,
    },
  })
  const { reset } = form
  useEffect(() => {
    reset({
      limit: account ? creditLimitToInput(account.credit_limit) : '',
      termDays: String(account?.payment_term_days ?? 0),
      status: account?.status === 'suspended' ? 'suspended' : 'active',
      emailEnabled: Boolean(account?.email_enabled),
    })
  }, [account, reset])
  const mutation = useMutation({
    mutationFn: async (values: CreditTermsFormValues) => {
      const creditLimit = creditLimitFromInput(values.limit)
      if (!account || creditLimit === null) {
        throw new Error(t('Enter a valid credit limit and payment term.'))
      }
      return requireServerSuccess(
        await saveCreditAccount(account.user_id, {
          credit_limit: creditLimit,
          payment_term_days: Number(values.termDays),
          status: values.status,
          email_enabled: values.emailEnabled,
        })
      )
    },
    onSuccess: (response) => {
      if (!response.data) {
        form.setError('root', { message: 'Failed to save credit terms' })
        return
      }
      onSaved(response.data)
      toast.success(t('Credit terms saved'))
    },
    onError: (error) => {
      form.setError('root', { message: 'Failed to save credit terms' })
      handleServerError(error, t('Failed to save credit terms'))
    },
  })
  return {
    form,
    mutation,
    submit: form.handleSubmit((values) => mutation.mutate(values)),
  }
}
