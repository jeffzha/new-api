import { Copy, ExternalLink } from 'lucide-react'
import { QRCodeSVG } from 'qrcode.react'
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
  const [payment, setPayment] = useState<Awaited<ReturnType<typeof requestAssistedPayment>> | null>(null)

  const reset = () => {
    setAmount('')
    setPaymentMethod('alipay_web')
    setPayment(null)
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
      setPayment(result)
      toast.success(t('Payment order created'))
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t('Failed to create assisted payment'))
    } finally {
      setLoading(false)
    }
  }

  const copyURL = async () => {
    if (!payment?.url) return
    await navigator.clipboard.writeText(paymentURL)
    toast.success(t('Copied'))
  }

  const paymentURL = (() => {
    if (!payment?.url) return ''
    try {
      const url = new URL(payment.url)
      Object.entries(payment.data ?? {}).forEach(([name, value]) => {
        url.searchParams.set(name, String(value))
      })
      return url.toString()
    } catch {
      return payment.url
    }
  })()

  const openPaymentPage = () => {
    if (!payment?.url) return
    const popup = window.open('', '_blank')
    if (!popup) {
      toast.error(t('Please allow pop-ups to open the payment page'))
      return
    }
    const form = popup.document.createElement('form')
    form.method = 'POST'
    form.action = payment.url
    form.style.display = 'none'
    Object.entries(payment.data ?? {}).forEach(([name, value]) => {
      const input = popup.document.createElement('input')
      input.type = 'hidden'
      input.name = name
      input.value = String(value)
      form.appendChild(input)
    })
    popup.document.body.appendChild(form)
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
        payment ? (
          <Button variant='outline' onClick={() => handleOpenChange(false)}>
            {t('Close')}
          </Button>
        ) : (
          <>
            <Button variant='outline' onClick={() => handleOpenChange(false)}>
              {t('Cancel')}
            </Button>
            <Button onClick={handleSubmit} disabled={loading}>
              {loading ? t('Processing...') : t('Create payment order')}
            </Button>
          </>
        )
      }
    >
      {payment ? (
        <div className='space-y-3'>
          <div className='flex justify-center rounded-md border bg-white p-4'>
            <QRCodeSVG value={paymentURL} size={220} aria-label={t('Payment URL')} />
          </div>
          <Label>{t('Payment URL')}</Label>
          <div className='flex gap-2'>
            <Input value={paymentURL} readOnly />
            <Button type='button' size='icon' variant='outline' onClick={copyURL} title={t('Copy')}>
              <Copy className='h-4 w-4' />
            </Button>
            <Button type='button' variant='outline' onClick={openPaymentPage} title={t('Open payment page')}>
              <ExternalLink className='h-4 w-4' />
              <span className='sr-only'>{t('Open payment page')}</span>
            </Button>
          </div>
          {payment.trade_no && (
            <p className='text-muted-foreground text-xs'>
              {t('Trade number')}: {payment.trade_no}
            </p>
          )}
        </div>
      ) : (
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
      )}
    </Dialog>
  )
}
