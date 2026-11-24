import { useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

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

type Operation = {
  request_id: string
  mode: string
  ops_amount: string
  reason: string
}

// Retain the exact operation through timeout, closing and page reload. A retry
// cannot silently become a new money operation with a different request ID.
export function OPSAdjustDialog(props: {
  open: boolean
  onOpenChange: (open: boolean) => void
  userId: number
  balance?: OPSBalance
  onSuccess: () => void
}) {
  const { t } = useTranslation()
  const operatorId = useAuthStore((s) => s.auth.user?.id)
  const queryClient = useQueryClient()
  const storageKey = `ops-balance-change:${operatorId}:${props.userId}`
  const [pending, setPending] = useState<Operation | null>(() => {
    try {
      return JSON.parse(
        sessionStorage.getItem(storageKey) || 'null'
      ) as Operation | null
    } catch {
      return null
    }
  })
  const [mode, setMode] = useState(pending?.mode || 'add')
  const [amount, setAmount] = useState(pending?.ops_amount || '')
  const [reason, setReason] = useState(pending?.reason || '')
  const [busy, setBusy] = useState(false)
  const validAmount =
    /^(0|[1-9]\d{0,12})(\.\d{1,6})?$/.test(amount) &&
    Number(amount) <= 1_000_000_000 &&
    (mode === 'override' || Number(amount) > 0)
  const submit = async () => {
    if (busy) return
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
            disabled={busy || (!pending && (!validAmount || !reason.trim()))}
          >
            {busy ? t('Processing...') : t('Confirm')}
          </Button>
        </>
      }
    >
      <div>
        <Label>{t('Balance')} (CNY)</Label>
        <OPSBalanceValue balance={props.balance} />
      </div>
      {pending && (
        <p className='text-muted-foreground text-sm'>
          {t(
            'An adjustment is awaiting confirmation. Confirm again to retry the same operation.'
          )}
        </p>
      )}
      <div className='flex gap-2'>
        {['add', 'subtract', 'override'].map((value) => {
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
        <Label htmlFor='ops-change-amount'>{t('Amount')} (CNY)</Label>
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
