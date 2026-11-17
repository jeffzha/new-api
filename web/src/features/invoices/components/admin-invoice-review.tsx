import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { RefreshCw } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Dialog } from '@/components/dialog'
import { EmptyState } from '@/components/empty-state'
import { SectionPageLayout } from '@/components/layout'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Pagination,
  PaginationContent,
  PaginationItem,
  PaginationNext,
  PaginationPrevious,
} from '@/components/ui/pagination'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Textarea } from '@/components/ui/textarea'
import { handleServerError } from '@/lib/handle-server-error'
import { requireServerSuccess } from '@/lib/server-error-message'

import {
  getAdminInvoiceApplications,
  issueInvoiceApplication,
  reviewInvoiceApplication,
  reviewInvoiceVoid,
} from '../api'
import type { InvoiceApplication, Paginated } from '../types'
import {
  ApplicationHistory,
  type AdminReviewAction,
} from './application-history'
import { InvoiceDetailsDialog } from './invoice-details-dialog'

function money(cents: number) {
  return `¥${(cents / 100).toFixed(2)}`
}

export function AdminInvoiceReview() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [status, setStatus] = useState('pending_review')
  const [page, setPage] = useState(1)
  const pageSize = 20
  const [detailItem, setDetailItem] = useState<InvoiceApplication | null>(null)
  const [approveItem, setApproveItem] = useState<InvoiceApplication | null>(
    null
  )
  const [reviewDialog, setReviewDialog] = useState<{
    item: InvoiceApplication
    action: Exclude<AdminReviewAction, 'approve'>
  } | null>(null)
  const [reviewReason, setReviewReason] = useState('')
  const [issueItem, setIssueItem] = useState<InvoiceApplication | null>(null)
  const [issueNumber, setIssueNumber] = useState('')
  const [issueCode, setIssueCode] = useState('')
  const [issueDate, setIssueDate] = useState('')
  const [issueFile, setIssueFile] = useState<File | null>(null)
  const applications = useQuery({
    queryKey: ['invoices', 'admin', status, page, pageSize],
    queryFn: async () =>
      requireServerSuccess(
        await getAdminInvoiceApplications(page, pageSize, status)
      ).data ?? { page, page_size: pageSize, total: 0, items: [] },
  })
  const refresh = () =>
    void queryClient.invalidateQueries({ queryKey: ['invoices', 'admin'] })
  const review = useMutation({
    mutationFn: async (input: {
      item: InvoiceApplication
      action: AdminReviewAction
      reason: string
    }) => {
      if (input.action === 'approve' || input.action === 'reject') {
        return requireServerSuccess(
          await reviewInvoiceApplication(
            input.item.id,
            input.action === 'approve',
            input.reason
          )
        )
      }
      return requireServerSuccess(
        await reviewInvoiceVoid(
          input.item.id,
          input.action === 'confirm_void',
          input.reason
        )
      )
    },
    onSuccess: () => {
      toast.success(t('Review saved'))
      setReviewDialog(null)
      setApproveItem(null)
      setReviewReason('')
      refresh()
      void queryClient.invalidateQueries({
        queryKey: ['invoice', 'admin-detail'],
      })
    },
    onError: (error) => handleServerError(error, t('Review failed')),
  })
  const resetIssueForm = () => {
    setIssueNumber('')
    setIssueCode('')
    setIssueDate('')
    setIssueFile(null)
  }
  const closeIssueDialog = () => {
    setIssueItem(null)
    resetIssueForm()
  }
  const issue = useMutation({
    mutationFn: async () => {
      if (!issueItem || !issueFile || !issueDate || !issueNumber.trim()) {
        throw new Error(t('Complete invoice number, date and PDF'))
      }
      return requireServerSuccess(
        await issueInvoiceApplication(issueItem.id, {
          amountCents: issueItem.total_cents,
          invoiceNumber: issueNumber.trim(),
          invoiceCode: issueCode.trim(),
          invoiceDate: issueDate,
          file: issueFile,
        })
      )
    },
    onSuccess: () => {
      toast.success(t('Invoice issued'))
      closeIssueDialog()
      refresh()
      void queryClient.invalidateQueries({
        queryKey: ['invoice', 'admin-detail'],
      })
    },
    onError: (error) => handleServerError(error, t('Issue failed')),
  })
  const startReview = (item: InvoiceApplication, action: AdminReviewAction) => {
    if (action === 'approve') {
      setApproveItem(item)
      return
    }
    setReviewReason('')
    setReviewDialog({ item, action })
  }
  const pageData = (applications.data ?? {
    page,
    page_size: pageSize,
    total: 0,
    items: [],
  }) as Paginated<InvoiceApplication>
  const totalPages = Math.max(1, Math.ceil((pageData.total ?? 0) / pageSize))
  const items = pageData.items ?? []
  let reviewDialogTitle = t('Reject void request')
  if (reviewDialog?.action === 'reject') {
    reviewDialogTitle = t('Reject invoice application')
  } else if (reviewDialog?.action === 'confirm_void') {
    reviewDialogTitle = t('Confirm invoice void')
  }
  return (
    <>
      <SectionPageLayout fixedContent>
        <SectionPageLayout.Title>{t('Invoice Review')}</SectionPageLayout.Title>
        <SectionPageLayout.Actions>
          <Button variant='outline' size='sm' onClick={refresh}>
            <RefreshCw className='size-4' />
            {t('Refresh')}
          </Button>
        </SectionPageLayout.Actions>
        <SectionPageLayout.Content>
          <div className='flex h-full min-h-0 flex-col gap-4'>
            <Tabs
              value={status}
              onValueChange={(value) => {
                setPage(1)
                setStatus(String(value))
              }}
            >
              <TabsList className='h-auto flex-wrap'>
                {[
                  ['pending_review', t('Pending review')],
                  ['approved', t('Waiting to issue')],
                  ['void_pending', t('Void requests')],
                  ['issued', t('Issued')],
                  ['', t('All')],
                ].map(([value, label]) => (
                  <TabsTrigger key={value} value={value}>
                    {label}
                  </TabsTrigger>
                ))}
              </TabsList>
            </Tabs>
            <div className='min-h-0 flex-1 overflow-y-auto'>
              {applications.isPending && <p role='status'>{t('Loading...')}</p>}
              {applications.isError && (
                <EmptyState
                  title={t('Failed to load invoices')}
                  description={t('Refresh the page and try again.')}
                />
              )}
              {applications.isSuccess && (
                <ApplicationHistory
                  applications={items}
                  admin
                  onDetail={setDetailItem}
                  onRefresh={refresh}
                  onReview={startReview}
                  onIssue={(item) => {
                    resetIssueForm()
                    setIssueItem(item)
                  }}
                  reviewPending={review.isPending}
                />
              )}
            </div>
            {pageData.total > 0 && (
              <Pagination>
                <PaginationContent>
                  <PaginationItem>
                    <PaginationPrevious
                      href='#'
                      text={t('Previous')}
                      aria-disabled={page <= 1}
                      className={
                        page <= 1 ? 'pointer-events-none opacity-50' : undefined
                      }
                      onClick={(event) => {
                        event.preventDefault()
                        if (page > 1) setPage((current) => current - 1)
                      }}
                    />
                  </PaginationItem>
                  <PaginationItem>
                    <span
                      className='text-muted-foreground px-3 text-sm'
                      aria-live='polite'
                    >
                      {t('Page {{page}} of {{totalPages}}', {
                        page,
                        totalPages,
                      })}
                    </span>
                  </PaginationItem>
                  <PaginationItem>
                    <PaginationNext
                      href='#'
                      text={t('Next')}
                      aria-disabled={page >= totalPages}
                      className={
                        page >= totalPages
                          ? 'pointer-events-none opacity-50'
                          : undefined
                      }
                      onClick={(event) => {
                        event.preventDefault()
                        if (page < totalPages) setPage((current) => current + 1)
                      }}
                    />
                  </PaginationItem>
                </PaginationContent>
              </Pagination>
            )}
          </div>
        </SectionPageLayout.Content>
      </SectionPageLayout>
      <InvoiceDetailsDialog
        applicationId={detailItem?.id ?? null}
        admin
        onOpenChange={(open) => {
          if (!open) setDetailItem(null)
        }}
      />
      <ConfirmDialog
        open={approveItem != null}
        onOpenChange={(open) => {
          if (!open && !review.isPending) setApproveItem(null)
        }}
        title={t('Approve invoice application')}
        desc={approveItem?.application_no ?? ''}
        confirmText={t('Approve')}
        isLoading={review.isPending}
        handleConfirm={() => {
          if (approveItem) {
            review.mutate({ item: approveItem, action: 'approve', reason: '' })
          }
        }}
      />
      <Dialog
        open={reviewDialog != null}
        onOpenChange={(open) => {
          if (!open) {
            setReviewDialog(null)
            setReviewReason('')
          }
        }}
        title={reviewDialogTitle}
        description={t(
          'A reason is required and will be stored in the audit history.'
        )}
        footer={
          <>
            <Button
              variant='outline'
              onClick={() => {
                setReviewDialog(null)
                setReviewReason('')
              }}
            >
              {t('Cancel')}
            </Button>
            <Button
              disabled={review.isPending || !reviewReason.trim()}
              onClick={() =>
                reviewDialog &&
                review.mutate({
                  item: reviewDialog.item,
                  action: reviewDialog.action,
                  reason: reviewReason.trim(),
                })
              }
            >
              {review.isPending ? t('Saving...') : t('Confirm')}
            </Button>
          </>
        }
      >
        <div className='space-y-1.5'>
          <Label htmlFor='invoice-review-reason' required>
            {t('Reason')}
          </Label>
          <Textarea
            id='invoice-review-reason'
            value={reviewReason}
            onChange={(event) => setReviewReason(event.target.value)}
            maxLength={1000}
          />
        </div>
      </Dialog>
      <Dialog
        open={issueItem != null}
        onOpenChange={(open) => !open && !issue.isPending && closeIssueDialog()}
        title={t('Issue invoice')}
        description={t(
          'Upload the PDF generated by the compliant external tax system.'
        )}
        footer={
          <>
            <Button
              variant='outline'
              disabled={issue.isPending}
              onClick={closeIssueDialog}
            >
              {t('Cancel')}
            </Button>
            <Button
              disabled={
                issue.isPending ||
                !issueNumber.trim() ||
                !issueDate ||
                !issueFile
              }
              onClick={() => issue.mutate()}
            >
              {issue.isPending ? t('Issuing...') : t('Confirm issue')}
            </Button>
          </>
        }
      >
        <div className='space-y-4'>
          {issueItem && (
            <div className='bg-muted/50 rounded-md p-3 text-sm'>
              <div className='flex items-center justify-between'>
                <span>{t('Application amount')}</span>
                <strong>{money(issueItem.total_cents)}</strong>
              </div>
              <p className='text-muted-foreground mt-1'>
                {t(
                  'The issued invoice amount must match this application amount.'
                )}
              </p>
            </div>
          )}
          <div className='space-y-1.5'>
            <Label htmlFor='invoice-number' required>
              {t('Invoice number')}
            </Label>
            <Input
              id='invoice-number'
              maxLength={128}
              value={issueNumber}
              onChange={(event) => setIssueNumber(event.target.value)}
            />
          </div>
          <div className='space-y-1.5'>
            <Label htmlFor='invoice-code'>{t('Invoice code')}</Label>
            <Input
              id='invoice-code'
              maxLength={128}
              value={issueCode}
              onChange={(event) => setIssueCode(event.target.value)}
            />
          </div>
          <div className='space-y-1.5'>
            <Label htmlFor='invoice-date' required>
              {t('Invoice date')}
            </Label>
            <Input
              id='invoice-date'
              type='date'
              value={issueDate}
              onChange={(event) => setIssueDate(event.target.value)}
            />
          </div>
          <div className='space-y-1.5'>
            <Label htmlFor='invoice-pdf' required>
              {t('Invoice PDF')}
            </Label>
            <Input
              id='invoice-pdf'
              type='file'
              accept='application/pdf,.pdf'
              onChange={(event) =>
                setIssueFile(event.target.files?.[0] ?? null)
              }
            />
          </div>
        </div>
      </Dialog>
    </>
  )
}
