import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { FileCheck2 } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { EmptyState } from '@/components/empty-state'
import { SectionPageLayout } from '@/components/layout'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { handleServerError } from '@/lib/handle-server-error'
import { requireServerSuccess } from '@/lib/server-error-message'

import {
  createInvoiceApplication,
  getInvoiceSources,
  getInvoiceSummary,
} from './api'
import { AdminInvoiceReview } from './components/admin-invoice-review'
import { ApplicationHistory } from './components/application-history'
import { InvoiceSummaryCards } from './components/invoice-summary-cards'
import { SourceSelector } from './components/source-selector'
import {
  headerSchema,
  invoiceAllocations,
  type HeaderForm,
} from './lib/validation'

function UserInvoices() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [amounts, setAmounts] = useState<Record<number, string>>({})
  const summary = useQuery({
    queryKey: ['invoice-summary'],
    queryFn: async () => requireServerSuccess(await getInvoiceSummary()).data,
  })
  const sources = useQuery({
    queryKey: ['invoice-sources'],
    queryFn: async () =>
      requireServerSuccess(await getInvoiceSources()).data ?? [],
  })
  const allocations = useMemo(
    () => invoiceAllocations(sources.data ?? [], amounts),
    [sources.data, amounts]
  )
  const form = useForm<HeaderForm>({
    resolver: zodResolver(headerSchema),
    mode: 'onBlur',
    defaultValues: { receiver_email: '' },
  })
  const submit = useMutation({
    mutationFn: async (values: HeaderForm) => {
      if (!allocations.valid) throw new Error(t('Check invoice source amounts'))
      return requireServerSuccess(
        await createInvoiceApplication({ ...values, lines: allocations.lines })
      )
    },
    onSuccess: () => {
      toast.success(t('Invoice application submitted'))
      setAmounts({})
      form.reset({ receiver_email: form.getValues('receiver_email') })
      void queryClient.invalidateQueries({ queryKey: ['invoice-summary'] })
      void queryClient.invalidateQueries({ queryKey: ['invoice-sources'] })
      void queryClient.invalidateQueries({ queryKey: ['invoices', 'mine'] })
    },
    onError: (error) =>
      handleServerError(error, t('Invoice application failed')),
  })
  const setAmount = (id: number, value: string) =>
    setAmounts((current) => ({ ...current, [id]: value }))
  const toggleSource = (id: number, selected: boolean) =>
    setAmounts((current) => {
      const next = { ...current }
      if (selected) {
        const source = sources.data?.find((item) => item.topup_id === id)
        if (source) next[id] = (source.available_cents / 100).toFixed(2)
      } else {
        delete next[id]
      }
      return next
    })

  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>{t('My Invoices')}</SectionPageLayout.Title>
      <SectionPageLayout.Content>
        <div className='flex flex-col gap-4'>
          {summary.isError && (
            <EmptyState
              title={t('Failed to load invoice summary')}
              description={t('Refresh the page and try again.')}
            />
          )}
          <InvoiceSummaryCards
            summary={summary.data}
            loading={summary.isPending}
          />
          {summary.isSuccess && (summary.data?.available_cents ?? 0) <= 0 && (
            <Alert>
              <AlertTitle>{t('Why is the invoiceable amount zero?')}</AlertTitle>
              <AlertDescription>
                {t(
                  'An invoice can only be issued against a verified payment receipt. Top-ups without a payment reference, such as historical or manually credited amounts, cannot be invoiced automatically. Model usage, gift balance, redemption codes and credit debt are not recharge receipts.'
                )}
              </AlertDescription>
            </Alert>
          )}
          <div className='grid gap-4 xl:grid-cols-[minmax(0,1.2fr)_minmax(320px,0.8fr)]'>
            <Card>
              <CardHeader>
                <CardTitle>{t('1. Select invoice sources')}</CardTitle>
                <CardDescription>
                  {t(
                    'Choose verified top-up payments and assign the amount to invoice.'
                  )}
                </CardDescription>
              </CardHeader>
              <CardContent>
                {sources.isPending && <p role='status'>{t('Loading...')}</p>}
                {sources.isError && (
                  <EmptyState
                    title={t('Failed to load invoice sources')}
                    description={t('Refresh the page and try again.')}
                  />
                )}
                {sources.isSuccess && (
                  <SourceSelector
                    sources={sources.data ?? []}
                    amounts={amounts}
                    errors={allocations.errors}
                    onChange={setAmount}
                    onToggle={toggleSource}
                  />
                )}
              </CardContent>
            </Card>
            <Card className='h-fit'>
              <CardHeader>
                <CardTitle>{t('2. Buyer information')}</CardTitle>
                <CardDescription>
                  {t(
                    'Financial staff will use this snapshot to issue the invoice.'
                  )}
                </CardDescription>
              </CardHeader>
              <CardContent>
                <form
                  className='space-y-4'
                  onSubmit={form.handleSubmit((values) =>
                    submit.mutate(values)
                  )}
                >
                  {(
                    [
                      ['purchaser_name', t('Purchaser name'), true],
                      ['tax_number', t('Tax ID'), true],
                      ['receiver_email', t('Receiving email'), true],
                      ['bank_name', t('Bank name'), false],
                      ['bank_account', t('Bank account'), false],
                      ['address', t('Address'), false],
                      ['phone', t('Phone'), false],
                    ] as const
                  ).map(([name, label, required]) => (
                    <div key={name} className='space-y-1.5'>
                      <Label htmlFor={`invoice-${name}`} required={required}>
                        {label}
                      </Label>
                      <Input
                        id={`invoice-${name}`}
                        {...form.register(name)}
                        aria-invalid={Boolean(form.formState.errors[name])}
                      />
                      {form.formState.errors[name] && (
                        <p className='text-destructive text-xs'>
                          {t('Please check this field')}
                        </p>
                      )}
                    </div>
                  ))}
                  <div className='space-y-1.5'>
                    <Label htmlFor='invoice-remark'>{t('Remark')}</Label>
                    <Textarea
                      id='invoice-remark'
                      {...form.register('remark')}
                    />
                  </div>
                  <div className='bg-muted/50 rounded-lg p-3 text-sm'>
                    <div className='flex items-center justify-between'>
                      <span>{t('This application')}</span>
                      <strong>
                        ¥{(allocations.totalCents / 100).toFixed(2)}
                      </strong>
                    </div>
                    <p className='text-muted-foreground mt-1 text-xs'>
                      {t(
                        'Only verified paid amounts are eligible. Approval is required before issue.'
                      )}
                    </p>
                  </div>
                  <Button
                    type='submit'
                    disabled={
                      submit.isPending ||
                      !allocations.valid ||
                      !sources.isSuccess
                    }
                    className='w-full'
                  >
                    <FileCheck2 className='size-4' />
                    {submit.isPending
                      ? t('Submitting...')
                      : t('Submit application')}
                  </Button>
                </form>
              </CardContent>
            </Card>
          </div>
          <Card>
            <CardHeader>
              <CardTitle>{t('Invoice history')}</CardTitle>
              <CardDescription>
                {t('Track review, issue and document delivery status.')}
              </CardDescription>
            </CardHeader>
            <CardContent>
              <ApplicationHistory />
            </CardContent>
          </Card>
        </div>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}

export function Invoices(props: { admin?: boolean }) {
  return props.admin ? <AdminInvoiceReview /> : <UserInvoices />
}
