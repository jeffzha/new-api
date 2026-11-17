import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { EmptyState } from '@/components/empty-state'
import { Badge } from '@/components/ui/badge'
import { Separator } from '@/components/ui/separator'
import { requireServerSuccess } from '@/lib/server-error-message'

import { getInvoiceApplication } from '../api'
import type { InvoiceApplication } from '../types'
import { InvoiceDownloadButton } from './invoice-download-button'
import {
  getInvoiceEmailLabel,
  getInvoiceStatusLabel,
  getInvoiceStatusVariant,
} from './invoice-status'

function money(cents: number) {
  return `¥${(cents / 100).toFixed(2)}`
}

function actionLabel(action: string, t: (key: string) => string) {
  switch (action) {
    case 'submit':
      return t('Application submitted')
    case 'review_approve':
      return t('Application approved')
    case 'review_reject':
      return t('Application rejected')
    case 'withdraw':
      return t('Application withdrawn')
    case 'issue':
      return t('Invoice issued')
    case 'void_request':
      return t('Void requested')
    case 'void_confirm':
      return t('Invoice void confirmed')
    case 'void_reject':
      return t('Void request rejected')
    case 'document_download':
      return t('Invoice PDF downloaded')
    default:
      return action
  }
}

function InvoiceDetails(props: {
  application: InvoiceApplication
  admin: boolean
}) {
  const { t } = useTranslation()
  const application = props.application
  const fields = [
    [t('Purchaser name'), application.purchaser_name],
    [t('Tax ID'), application.tax_number],
    [t('Receiving email'), application.receiver_email],
    [t('Bank name'), application.bank_name],
    [t('Bank account'), application.bank_account],
    [t('Address'), application.address],
    [t('Phone'), application.phone],
    [t('Remark'), application.remark],
  ] as const
  return (
    <div className='space-y-5'>
      <div className='flex flex-wrap items-center gap-2'>
        <span className='font-medium'>{application.application_no}</span>
        <Badge variant={getInvoiceStatusVariant(application.status)}>
          {getInvoiceStatusLabel(application.status, t)}
        </Badge>
        <span className='text-muted-foreground text-sm'>
          {money(application.total_cents)}
        </span>
        {props.admin && application.user_id != null && (
          <span className='text-muted-foreground text-xs'>
            {t('User ID')}: {application.user_id}
          </span>
        )}
      </div>
      <dl className='grid gap-3 sm:grid-cols-2'>
        {fields.map(([label, value]) => (
          <div key={label} className='min-w-0 space-y-1'>
            <dt className='text-muted-foreground text-xs'>{label}</dt>
            <dd className='text-sm break-words'>
              {value || t('Not provided')}
            </dd>
          </div>
        ))}
      </dl>
      {(application.invoice_number ||
        application.invoice_code ||
        application.invoice_date) && (
        <div className='space-y-3'>
          <Separator />
          <h4 className='font-medium'>{t('Issued invoice')}</h4>
          <div className='grid gap-3 sm:grid-cols-2'>
            <DetailField
              label={t('Invoice number')}
              value={application.invoice_number}
            />
            <DetailField
              label={t('Invoice code')}
              value={application.invoice_code}
            />
            <DetailField
              label={t('Invoice date')}
              value={
                application.invoice_date
                  ? new Date(
                      application.invoice_date * 1000
                    ).toLocaleDateString()
                  : undefined
              }
            />
            <DetailField
              label={t('Email delivery')}
              value={getInvoiceEmailLabel(application.email_status, t)}
            />
          </div>
          {application.has_document && (
            <InvoiceDownloadButton id={application.id} admin={props.admin} />
          )}
        </div>
      )}
      <div className='space-y-2'>
        <h4 className='font-medium'>{t('Invoice sources')}</h4>
        {application.lines.length === 0 ? (
          <p className='text-muted-foreground text-sm'>
            {t('No invoice sources')}
          </p>
        ) : (
          application.lines.map((line) => (
            <div
              key={line.id}
              className='flex flex-wrap items-center justify-between gap-2 rounded-md border p-3 text-sm'
            >
              <span className='break-all'>{line.source_trade_no}</span>
              <span>
                {line.allocated} / {t('Paid')} {line.paid}
              </span>
            </div>
          ))
        )}
      </div>
      {application.review_reason && (
        <div className='bg-muted/50 rounded-md p-3 text-sm'>
          <span className='font-medium'>{t('Review reason')}: </span>
          {application.review_reason}
        </div>
      )}
      {application.events && application.events.length > 0 && (
        <div className='space-y-2'>
          <Separator />
          <h4 className='font-medium'>{t('Audit history')}</h4>
          <div className='space-y-2'>
            {application.events.map((event) => (
              <div key={event.id} className='rounded-md border p-3 text-sm'>
                <div className='flex flex-wrap items-center justify-between gap-2'>
                  <span className='font-medium'>
                    {actionLabel(event.action, t)}
                  </span>
                  <span className='text-muted-foreground text-xs'>
                    {new Date(event.created_at * 1000).toLocaleString()}
                  </span>
                </div>
                {event.reason && (
                  <p className='text-muted-foreground mt-1 break-words'>
                    {event.reason}
                  </p>
                )}
                {event.from_status && event.to_status && (
                  <p className='text-muted-foreground mt-1 text-xs'>
                    {getInvoiceStatusLabel(
                      event.from_status as InvoiceApplication['status'],
                      t
                    )}{' '}
                    →{' '}
                    {getInvoiceStatusLabel(
                      event.to_status as InvoiceApplication['status'],
                      t
                    )}
                  </p>
                )}
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}

function DetailField(props: { label: string; value?: string }) {
  const { t } = useTranslation()
  return (
    <div className='min-w-0 space-y-1'>
      <dt className='text-muted-foreground text-xs'>{props.label}</dt>
      <dd className='text-sm break-words'>
        {props.value || t('Not provided')}
      </dd>
    </div>
  )
}

export function InvoiceDetailsDialog(props: {
  applicationId: number | null
  admin?: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const application = useQuery({
    queryKey: [
      'invoice',
      props.admin ? 'admin-detail' : 'user-detail',
      props.applicationId,
    ],
    enabled: props.applicationId != null,
    queryFn: async () => {
      if (props.applicationId == null) return null
      return (
        requireServerSuccess(
          await getInvoiceApplication(props.applicationId, props.admin)
        ).data ?? null
      )
    },
  })
  const title = application.data?.application_no ?? t('Invoice details')
  let content = null
  if (application.isPending) {
    content = (
      <p role='status' className='text-muted-foreground text-sm'>
        {t('Loading...')}
      </p>
    )
  } else if (application.isError) {
    content = (
      <EmptyState
        title={t('Failed to load invoice details')}
        description={t('Refresh the page and try again.')}
      />
    )
  } else if (application.data) {
    content = (
      <InvoiceDetails
        application={application.data}
        admin={Boolean(props.admin)}
      />
    )
  }
  return (
    <Dialog
      open={props.applicationId != null}
      onOpenChange={props.onOpenChange}
      title={title}
      description={t(
        'Review application information, invoice sources and audit events.'
      )}
    >
      {content}
    </Dialog>
  )
}
