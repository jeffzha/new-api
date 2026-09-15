import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'

import { requestAssistedPayment } from '../api'

interface AssistedPaymentDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  userId: number
}

export function AssistedPaymentDialog({
  open,
  onOpenChange,
  userId,
}: AssistedPaymentDialogProps) {
  const { t } = useTranslation()
  const [amount, setAmount] = useState('')
  const [paymentMethod, setPaymentMethod] = useState('alipay_web')
  const [loading, setLoading] = useState(false)

  const reset = () => {
    setAmount('')
    setPaymentMethod('alipay_web')
  }

  const handleOpenChange = (next: boolean) => {
    if (!next) reset()
    onOpenChange(next)
  }

  const handleSubmit = async () => {
    const normalizedAmount = amount.trim()
    if (!/^\d+(?:\.\d{1,2})?$/.test(normalizedAmount) || Number(normalizedAmount) <= 0) {
      toast.error(t('Enter a valid amount'))
      return
    }
    if (!paymentMethod.trim()) {
      toast.error(t('Enter a payment method'))
      return
    }
    setLoading(true)
    try {
      const result = await requestAssistedPayment(userId, {
        money: normalizedAmount,
        payment_method: paymentMethod.trim(),
      })
      const succeeded = result.success === true || result.message === 'success'
      if (!succeeded || !result.url) {
        toast.error(result.message || t('Failed to create assisted payment'))
        return
      }
      // Match the normal wallet top-up flow: submit the signed order to Epay
      // in the current tab so Epay renders its own WeChat/Alipay checkout QR.
      submitPaymentForm(result)
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t('Failed to create assisted payment'))
    } finally {
      setLoading(false)
    }
  }

  const submitPaymentForm = (order: {
    url: string
    data?: Record<string, string>
  }) => {
    const form = document.createElement('form')
    form.method = 'POST'
    form.action = order.url
    form.style.display = 'none'
    Object.entries(order.data ?? {}).forEach(([name, value]) => {
      const input = document.createElement('input')
      input.type = 'hidden'
      input.name = name
      input.value = String(value)
      form.appendChild(input)
    })
    document.body.appendChild(form)
    form.submit()
  }

  return (
    <Dialog
      open={open}
      onOpenChange={handleOpenChange}
      title={t('Assisted payment')}
      description={t('Create a payment order for this user')}
      contentHeight='auto'
      bodyClassName='space-y-4'
      footer={
        <>
          <Button variant='outline' onClick={() => handleOpenChange(false)}>
            {t('Cancel')}
          </Button>
          <Button onClick={handleSubmit} disabled={loading}>
            {loading ? t('Processing...') : t('Create payment order')}
          </Button>
        </>
      }
    >
      <div className='space-y-4'>
          <div className='space-y-2'>
            <Label htmlFor='assisted-payment-amount'>
              {t('Amount')} (CNY)
            </Label>
            <Input
              id='assisted-payment-amount'
              type='number'
              min='0'
              step='0.01'
              value={amount}
              onChange={(event) => setAmount(event.target.value)}
              placeholder={t('Enter amount')}
            />
          </div>
          <div className='space-y-2'>
            <Label htmlFor='assisted-payment-method'>{t('Payment method')}</Label>
            <select
              id='assisted-payment-method'
              value={paymentMethod}
              onChange={(event) => setPaymentMethod(event.target.value)}
            >
              <option value='alipay_web'>{t('Alipay')}</option>
              <option value='wxpay'>{t('WeChat Pay')}</option>
            </select>
          </div>
      </div>
    </Dialog>
  )
}
