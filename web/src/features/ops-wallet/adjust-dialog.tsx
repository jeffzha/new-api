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
import { useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { z } from 'zod'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { api } from '@/lib/api'
import { handleServerError } from '@/lib/handle-server-error'
import { requireServerSuccess } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import { OPSBalanceValue } from './balance-value'
import type { OPSBalance } from './types'

const operationSchema = z
  .object({
    request_id: z.string().regex(/^[A-Za-z0-9_-]{1,64}$/),
    mode: z.enum(['add', 'subtract', 'override']),
    ops_amount: z.string().regex(/^(0|[1-9]\d{0,12})(\.\d{1,6})?$/),
    reason: z.string().min(1).max(500),
  })
  .refine(
    (operation) =>
      operation.reason.trim() !== '' &&
      Number(operation.ops_amount) <= 1_000_000_000 &&
      (operation.mode === 'override' || Number(operation.ops_amount) > 0)
  )

type Operation = z.infer<typeof operationSchema>
type AdjustmentProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  userId: number
  balance?: OPSBalance
  onSuccess: () => void
}

export function OPSAdjustDialog(props: AdjustmentProps) {
  const operatorId = useAuthStore((s) => s.auth.user?.id)
  return (
    <OPSAdjustForm
      key={`${operatorId}:${props.userId}`}
      {...props}
      operatorId={operatorId}
    />
  )
}

// Retain the exact operation through timeout, closing and page reload. A retry
// cannot silently become a new money operation with a different request ID.
function OPSAdjustForm(props: AdjustmentProps & { operatorId?: number }) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const storageKey = `ops-balance-change:${props.operatorId}:${props.userId}`
  const [saved] = useState(() => {
    try {
      const raw = sessionStorage.getItem(storageKey)
      if (raw === null) return { operation: null, blocked: false }
      const parsed = operationSchema.safeParse(JSON.parse(raw))
      if (parsed.success) return { operation: parsed.data, blocked: false }
    } catch {
      // An unreadable pending record might already have reached OPS.
    }
    return { operation: null, blocked: true }
  })
  const [pending, setPending] = useState<Operation | null>(saved.operation)
  const [mode, setMode] = useState(pending?.mode || 'add')
  const [amount, setAmount] = useState(pending?.ops_amount || '')
  const [reason, setReason] = useState(pending?.reason || '')
  const [busy, setBusy] = useState(false)
  const validAmount =
    /^(0|[1-9]\d{0,12})(\.\d{1,6})?$/.test(amount) &&
    Number(amount) <= 1_000_000_000 &&
    (mode === 'override' || Number(amount) > 0)
  const submit = async () => {
    if (busy || saved.blocked || !props.operatorId) return
    if (!pending && (!validAmount || !reason.trim())) {
      return
    }
    setBusy(true)
    try {
      const operation = pending || {
        request_id: `adm-${crypto.randomUUID()}`,
        mode,
        ops_amount: amount,
        reason: reason.trim(),
      }
      // Storage failure prevents the request, rather than risking an untracked
      // retry of an operation that might already have changed the balance.
      sessionStorage.setItem(storageKey, JSON.stringify(operation))
      setPending(operation)
      const response = await api.post('/api/user/manage', {
        id: props.userId,
        action: 'add_quota',
        value: 0,
        ...operation,
      })
      if (
        response.data.success === false &&
        response.data.retry_same_operation === false
      ) {
        sessionStorage.removeItem(storageKey)
        setPending(null)
      }
      requireServerSuccess(response.data)
      sessionStorage.removeItem(storageKey)
      setPending(null)
      setAmount('')
      setReason('')
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['users'] }),
        queryClient.invalidateQueries({ queryKey: ['wallet'] }),
        queryClient.invalidateQueries({
          queryKey: ['dashboard', 'wallet-balance'],
        }),
      ])
      toast.success(t('Quota adjusted successfully'))
      props.onSuccess()
      props.onOpenChange(false)
    } catch (error) {
      handleServerError(error, t('Failed to adjust quota'))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Dialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('Adjust Quota')}
      description={t(
        'Balance changes are recorded in OPS. Local balance is unchanged.'
      )}
      contentHeight='auto'
      bodyClassName='space-y-4'
      footer={
        <>
          <Button
            variant='outline'
            onClick={() => props.onOpenChange(false)}
            disabled={busy}
          >
            {t('Cancel')}
          </Button>
          <Button
            onClick={submit}
            disabled={
              busy ||
              saved.blocked ||
              !props.operatorId ||
              (!pending && (!validAmount || !reason.trim()))
            }
          >
            {busy ? t('Processing...') : t('Confirm')}
          </Button>
        </>
      }
    >
      <div>
        <Label>
          {t('Balance')} ({props.balance?.currency || 'USD'})
        </Label>
        <OPSBalanceValue balance={props.balance} />
      </div>
      {pending && (
        <p className='text-muted-foreground text-sm'>
          {t(
            'An adjustment is awaiting confirmation. Confirm again to retry the same operation.'
          )}
        </p>
      )}
      {saved.blocked && (
        <p role='alert' className='text-destructive text-sm'>
          {t(
            'A saved balance adjustment could not be read. Contact an administrator before trying again.'
          )}
        </p>
      )}
      <div className='flex gap-2'>
        {(['add', 'subtract', 'override'] as const).map((value) => {
          let label = t('Override')
          if (value === 'add') label = t('Add')
          if (value === 'subtract') label = t('Subtract')
          return (
            <Button
              key={value}
              variant={mode === value ? 'default' : 'outline'}
              disabled={busy || !!pending}
              onClick={() => setMode(value)}
            >
              {label}
            </Button>
          )
        })}
      </div>
      <div className='space-y-2'>
        <Label htmlFor='ops-change-amount'>
          {t('Amount')} ({props.balance?.currency || 'USD'})
        </Label>
        <Input
          id='ops-change-amount'
          inputMode='decimal'
          aria-invalid={amount !== '' && !validAmount}
          placeholder={t('Enter amount')}
          value={amount}
          onChange={(e) => setAmount(e.target.value)}
          disabled={busy || !!pending}
        />
      </div>
      <div className='space-y-2'>
        <Label htmlFor='ops-change-reason'>{t('Reason')}</Label>
        <Textarea
          id='ops-change-reason'
          value={reason}
          maxLength={500}
          onChange={(e) => setReason(e.target.value)}
          disabled={busy || !!pending}
        />
      </div>
    </Dialog>
  )
}
