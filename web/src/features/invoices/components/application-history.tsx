import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Eye, Send, ShieldCheck, XCircle } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Dialog } from '@/components/dialog'
import { EmptyState } from '@/components/empty-state'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import {
  Pagination,
  PaginationContent,
  PaginationItem,
} from '@/components/ui/pagination'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { handleServerError } from '@/lib/handle-server-error'
import { requireServerSuccess } from '@/lib/server-error-message'

import {
  getInvoiceApplications,
  requestInvoiceVoid,
  resendInvoiceEmail,
  withdrawInvoiceApplication,
} from '../api'
import type { InvoiceApplication, InvoiceStatus } from '../types'
import { InvoiceDetailsDialog } from './invoice-details-dialog'
import { InvoiceDownloadButton } from './invoice-download-button'
import {
  getInvoiceEmailLabel,
  getInvoiceStatusLabel,
  getInvoiceStatusVariant,
} from './invoice-status'

export type AdminReviewAction =
  | 'approve'
  | 'reject'
  | 'confirm_void'
  | 'reject_void'

function money(cents: number) {
  return `¥${(cents / 100).toFixed(2)}`
}

export function ApplicationHistory(props: {
  applications?: InvoiceApplication[]
  admin?: boolean
  onRefresh?: () => void
  onDetail?: (item: InvoiceApplication) => void
  onReview?: (item: InvoiceApplication, action: AdminReviewAction) => void
  onIssue?: (item: InvoiceApplication) => void
  reviewPending?: boolean
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [voidItem, setVoidItem] = useState<InvoiceApplication | null>(null)
  const [voidReason, setVoidReason] = useState('')
  const [withdrawItem, setWithdrawItem] = useState<InvoiceApplication | null>(
    null
  )
  const [detailId, setDetailId] = useState<number | null>(null)
  const [page, setPage] = useState(1)
  const [status, setStatus] = useState<InvoiceStatus | 'all'>('all')
  const applicationsQuery = useQuery({
    queryKey: ['invoices', 'mine', page, status],
    queryFn: async () =>
      requireServerSuccess(
        await getInvoiceApplications(page, 10, status === 'all' ? '' : status)
      ).data,
    enabled: !props.admin && props.applications == null,
  })
  const applications = props.applications ?? applicationsQuery.data?.items ?? []
  const totalPages = Math.max(
    1,
    Math.ceil((applicationsQuery.data?.total ?? 0) / 10)
  )
  const refresh = () => {
    props.onRefresh?.()
    void queryClient.invalidateQueries({ queryKey: ['invoices', 'mine'] })
    void queryClient.invalidateQueries({ queryKey: ['invoice-summary'] })
    void queryClient.invalidateQueries({ queryKey: ['invoice-sources'] })
    void queryClient.invalidateQueries({ queryKey: ['invoice'] })
  }
  const action = useMutation({
    mutationFn: async (input: {
      kind: 'withdraw' | 'resend'
      item: InvoiceApplication
    }) => {
      if (input.kind === 'withdraw') {
        return requireServerSuccess(
          await withdrawInvoiceApplication(input.item.id)
        )
      }
      return requireServerSuccess(await resendInvoiceEmail(input.item.id))
    },
    onSuccess: () => {
      toast.success(t('Invoice status updated'))
      setWithdrawItem(null)
      refresh()
    },
    onError: (error) => handleServerError(error, t('Invoice action failed')),
  })
  const voidMutation = useMutation({
    mutationFn: async (input: { item: InvoiceApplication; reason: string }) =>
      requireServerSuccess(
        await requestInvoiceVoid(input.item.id, input.reason)
      ),
    onSuccess: () => {
      toast.success(t('Invoice status updated'))
      setVoidItem(null)
      setVoidReason('')
      refresh()
    },
    onError: (error) => handleServerError(error, t('Invoice action failed')),
  })
  if (!props.admin && applicationsQuery.isPending) {
    return <p className='text-muted-foreground text-sm'>{t('Loading...')}</p>
  }
  if (!props.admin && applicationsQuery.isError) {
    return (
      <EmptyState
        title={t('Failed to load invoices')}
        description={t('Refresh the page and try again.')}
      />
    )
  }
  return (
    <>
      {!props.admin && (
        <Select
          value={status}
          onValueChange={(value) => {
            if (value) {
              setStatus(value)
              setPage(1)
            }
          }}
        >
          <SelectTrigger className='mb-3 w-56' aria-label={t('Invoice status')}>
            <SelectValue>
              {status === 'all' ? t('All') : getInvoiceStatusLabel(status, t)}
            </SelectValue>
          </SelectTrigger>
          <SelectContent>
            <SelectItem value='all'>{t('All')}</SelectItem>
            {(
              [
                'pending_review',
                'approved',
                'issued',
                'rejected',
                'withdrawn',
                'void_pending',
                'voided',
              ] as const
            ).map((value) => (
              <SelectItem key={value} value={value}>
                {getInvoiceStatusLabel(value, t)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      )}
      {applications.length === 0 && (
        <EmptyState
          title={t('No invoice applications yet')}
          description={t('Submitted applications will appear here.')}
        />
      )}
      <div className='space-y-3'>
        {applications.map((item) => (
          <div
            key={item.id}
            className='flex flex-col gap-3 rounded-lg border p-4 lg:flex-row lg:items-center'
          >
            <div className='min-w-0 flex-1'>
              <div className='flex flex-wrap items-center gap-2'>
                <span className='font-medium break-all'>
                  {item.application_no}
                </span>
                <Badge variant={getInvoiceStatusVariant(item.status)}>
                  {getInvoiceStatusLabel(item.status, t)}
                </Badge>
                {item.has_document && (
                  <Badge
                    variant={
                      item.email_status === 'failed' ? 'destructive' : 'outline'
                    }
                  >
                    {getInvoiceEmailLabel(item.email_status, t)}
                  </Badge>
                )}
              </div>
              <p className='text-muted-foreground mt-1 text-sm break-words'>
                {item.purchaser_name}
              </p>
              <p className='text-muted-foreground text-xs'>
                {new Date(item.created_at * 1000).toLocaleString()}
              </p>
            </div>
            <div className='text-lg font-semibold'>
              {money(item.total_cents)}
            </div>
            <div className='flex flex-wrap gap-2'>
              <Button
                size='sm'
                variant='ghost'
                onClick={() =>
                  props.onDetail ? props.onDetail(item) : setDetailId(item.id)
                }
              >
                <Eye className='size-4' />
                {t('Details')}
              </Button>
              {!props.admin && item.status === 'pending_review' && (
                <Button
                  size='sm'
                  variant='outline'
                  disabled={action.isPending}
                  onClick={() => setWithdrawItem(item)}
                >
                  {t('Withdraw')}
                </Button>
              )}
              {!props.admin && item.status === 'issued' && (
                <Button
                  size='sm'
                  variant='outline'
                  onClick={() => {
                    setVoidItem(item)
                    setVoidReason('')
                  }}
                >
                  {t('Request void')}
                </Button>
              )}
              {props.admin &&
                item.status === 'pending_review' &&
                props.onReview && (
                  <>
                    <Button
                      size='sm'
                      disabled={props.reviewPending}
                      onClick={() => props.onReview?.(item, 'approve')}
                    >
                      <ShieldCheck className='size-4' />
                      {t('Approve')}
                    </Button>
                    <Button
                      size='sm'
                      variant='destructive'
                      disabled={props.reviewPending}
                      onClick={() => props.onReview?.(item, 'reject')}
                    >
                      <XCircle className='size-4' />
                      {t('Reject')}
                    </Button>
                  </>
                )}
              {props.admin && item.status === 'approved' && props.onIssue && (
                <Button size='sm' onClick={() => props.onIssue?.(item)}>
                  {t('Issue invoice')}
                </Button>
              )}
              {props.admin &&
                item.status === 'void_pending' &&
                props.onReview && (
                  <>
                    <Button
                      size='sm'
                      disabled={props.reviewPending}
                      onClick={() => props.onReview?.(item, 'confirm_void')}
                    >
                      {t('Confirm void')}
                    </Button>
                    <Button
                      size='sm'
                      variant='outline'
                      disabled={props.reviewPending}
                      onClick={() => props.onReview?.(item, 'reject_void')}
                    >
                      {t('Reject void')}
                    </Button>
                  </>
                )}
              {props.admin &&
                (item.status === 'issued' ||
                  item.status === 'void_pending') && (
                  <Button
                    size='sm'
                    variant='ghost'
                    disabled={action.isPending}
                    onClick={() => action.mutate({ kind: 'resend', item })}
                  >
                    <Send className='size-4' />
                    {t('Resend invoice email')}
                  </Button>
                )}
              {item.has_document && (
                <InvoiceDownloadButton id={item.id} admin={props.admin} />
              )}
            </div>
          </div>
        ))}
      </div>
      {!props.admin && totalPages > 1 && (
        <Pagination>
          <PaginationContent>
            <PaginationItem>
              <Button
                size='sm'
                variant='outline'
                disabled={page <= 1}
                onClick={() => setPage((current) => current - 1)}
              >
                {t('Previous')}
              </Button>
            </PaginationItem>
            <PaginationItem>
              <span className='px-3 text-sm'>
                {t('Page {{page}} of {{totalPages}}', { page, totalPages })}
              </span>
            </PaginationItem>
            <PaginationItem>
              <Button
                size='sm'
                variant='outline'
                disabled={page >= totalPages}
                onClick={() => setPage((current) => current + 1)}
              >
                {t('Next')}
              </Button>
            </PaginationItem>
          </PaginationContent>
        </Pagination>
      )}
      <InvoiceDetailsDialog
        applicationId={detailId}
        admin={props.admin}
        onOpenChange={(open) => {
          if (!open) setDetailId(null)
        }}
      />
      <ConfirmDialog
        open={withdrawItem != null}
        onOpenChange={(open) => {
          if (!open && !action.isPending) setWithdrawItem(null)
        }}
        title={t('Withdraw invoice application')}
        desc={t('The reserved amount will become available again.')}
        confirmText={t('Withdraw')}
        isLoading={action.isPending}
        handleConfirm={() => {
          if (withdrawItem) {
            action.mutate({ kind: 'withdraw', item: withdrawItem })
          }
        }}
      />
      <Dialog
        open={voidItem != null}
        onOpenChange={(open) => {
          if (!open) {
            setVoidItem(null)
            setVoidReason('')
          }
        }}
        title={t('Request invoice void')}
        description={t(
          'Explain why this issued invoice should be voided or red-issued.'
        )}
        footer={
          <>
            <Button
              variant='outline'
              onClick={() => {
                setVoidItem(null)
                setVoidReason('')
              }}
            >
              {t('Cancel')}
            </Button>
            <Button
              disabled={voidMutation.isPending || !voidReason.trim()}
              onClick={() =>
                voidItem &&
                voidMutation.mutate({
                  item: voidItem,
                  reason: voidReason.trim(),
                })
              }
            >
              {voidMutation.isPending
                ? t('Submitting...')
                : t('Submit request')}
            </Button>
          </>
        }
      >
        <div className='space-y-1.5'>
          <Label htmlFor='invoice-void-reason' required>
            {t('Reason')}
          </Label>
          <Textarea
            id='invoice-void-reason'
            value={voidReason}
            onChange={(event) => setVoidReason(event.target.value)}
            maxLength={1000}
          />
        </div>
      </Dialog>
    </>
  )
}
