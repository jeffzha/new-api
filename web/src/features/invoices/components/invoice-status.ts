import type { InvoiceStatus } from '../types'

type Translate = (key: string) => string

export function getInvoiceEmailLabel(status: string, t: Translate): string {
  if (status === 'sent') return t('Invoice email sent')
  if (status === 'failed') return t('Invoice email failed')
  return t('Invoice email pending')
}

export function getInvoiceStatusLabel(status: InvoiceStatus, t: Translate) {
  switch (status) {
    case 'pending_review':
      return t('Pending review')
    case 'rejected':
      return t('Rejected')
    case 'approved':
      return t('Approved, waiting to issue')
    case 'issued':
      return t('Issued')
    case 'withdrawn':
      return t('Withdrawn')
    case 'void_pending':
      return t('Void request pending')
    case 'voided':
      return t('Voided')
  }
}

export function getInvoiceStatusVariant(status: InvoiceStatus) {
  if (status === 'issued' || status === 'approved') return 'default' as const
  if (status === 'rejected' || status === 'voided') {
    return 'destructive' as const
  }
  return 'secondary' as const
}
