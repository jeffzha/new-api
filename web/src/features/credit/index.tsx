import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  CalendarDays,
  Check,
  Mail,
  RefreshCw,
  Search,
  ShieldAlert,
  WalletCards,
} from 'lucide-react'
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { EmptyState } from '@/components/empty-state'
import { SectionPageLayout } from '@/components/layout'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Textarea } from '@/components/ui/textarea'
import { handleServerError } from '@/lib/handle-server-error'
import { requireServerSuccess } from '@/lib/server-error-message'

import {
  getCreditAccount,
  listCreditAccounts,
  listCreditStatements,
  registerCreditPayment,
  resendCreditStatementEmail,
  resolveCreditAccount,
  saveCreditAccount,
  writeOffCreditStatement,
} from './api'
import {
  CREDIT_REPAYMENT_DAY,
  creditLimitFromInput,
  creditLimitToInput,
  formatCreditAmount,
  isStatementOpen,
  isStatementOverdue,
  statementStatusKey,
  emailStatusKey,
} from './lib/format'
import type { CreditAccount, CreditStatement } from './types'

function timestamp(value: number | undefined): string {
  if (!value) return '-'
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: 'medium',
    timeStyle: 'short',
  }).format(new Date(value))
}

function statusVariant(
  status: string
): 'default' | 'secondary' | 'destructive' | 'warning' {
  if (status === 'paid') return 'default'
  if (status === 'bad_debt') return 'destructive'
  if (status === 'overdue') return 'warning'
  return 'secondary'
}

export function CreditBook() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [search, setSearch] = useState('')
  const [selected, setSelected] = useState<CreditAccount | null>(null)
  const [collection, setCollection] = useState<CreditStatement | null>(null)
  const [badDebt, setBadDebt] = useState<CreditStatement | null>(null)

  const accounts = useQuery({
    queryKey: ['credit', 'accounts'],
    queryFn: async () =>
      requireServerSuccess(await listCreditAccounts()).data ?? { items: [] },
  })
  const accountItems = accounts.data?.items ?? []
  const statements = useQuery({
    queryKey: ['credit', 'statements', selected?.user_id],
    enabled: Boolean(selected),
    queryFn: async () => {
      if (!selected) return { items: [] }
      return (
        requireServerSuccess(await listCreditStatements(selected.user_id))
          .data ?? {
          items: [],
        }
      )
    },
  })

  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ['credit'] })
  }

  async function searchAccount(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const value = search.trim()
    if (!value) return
    try {
      const result = requireServerSuccess(await resolveCreditAccount(value))
      if (!result.data) throw new Error(t('Customer not found'))
      setSelected(result.data)
    } catch (error) {
      handleServerError(error, t('Customer lookup failed'))
    }
  }

  async function selectAccount(userId: string) {
    try {
      const result = requireServerSuccess(await getCreditAccount(userId))
      if (result.data) setSelected(result.data)
    } catch (error) {
      handleServerError(error, t('Failed to load credit account'))
    }
  }

  return (
    <SectionPageLayout fixedContent>
      <SectionPageLayout.Title>
        {t('Credit & statements')}
      </SectionPageLayout.Title>
      <SectionPageLayout.Actions>
        <Button variant='outline' size='sm' onClick={refresh}>
          <RefreshCw />
          {t('Refresh')}
        </Button>
      </SectionPageLayout.Actions>
      <SectionPageLayout.Content>
        <div className='flex min-h-0 flex-col gap-4 overflow-auto pr-1'>
          <Card>
            <CardHeader>
              <CardTitle>{t('Platform credit book')}</CardTitle>
              <CardDescription>
                {t(
                  'Credit is managed by the super administrator and is independent of agency ownership.'
                )}
              </CardDescription>
            </CardHeader>
            <CardContent>
              <form
                className='flex flex-col gap-2 sm:flex-row'
                onSubmit={searchAccount}
              >
                <div className='relative min-w-0 flex-1'>
                  <Search className='text-muted-foreground absolute top-1/2 left-2.5 size-4 -translate-y-1/2' />
                  <Input
                    aria-label={t('Customer account or user ID')}
                    className='pl-8'
                    value={search}
                    onChange={(event) => setSearch(event.target.value)}
                    placeholder={t('Customer account or user ID')}
                  />
                </div>
                <Button type='submit'>
                  <Search />
                  {t('Search')}
                </Button>
              </form>
            </CardContent>
          </Card>

          {selected ? (
            <CreditAccountPanel
              key={selected.user_id}
              account={selected}
              statements={statements.data?.items ?? []}
              statementsLoading={statements.isPending}
              onSaved={(account) => {
                setSelected(account)
                refresh()
              }}
              onCollect={setCollection}
              onBadDebt={setBadDebt}
            />
          ) : null}

          <Card className='min-h-0'>
            <CardHeader>
              <CardTitle>{t('Configured credit lines')}</CardTitle>
              <CardDescription>
                {t('Select a customer to view settings and statements.')}
              </CardDescription>
            </CardHeader>
            <CardContent className='min-h-0'>
              {accounts.isPending && (
                <p className='text-muted-foreground py-8 text-center'>
                  {t('Loading...')}
                </p>
              )}
              {!accounts.isPending && accountItems.length === 0 && (
                <EmptyState
                  className='min-h-48'
                  icon={WalletCards}
                  title={t('No credit terms yet')}
                  description={t(
                    'Search for a customer to set the first credit line.'
                  )}
                />
              )}
              {!accounts.isPending && accountItems.length > 0 && (
                <div className='overflow-x-auto'>
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>{t('Customer')}</TableHead>
                        <TableHead>{t('Credit limit')}</TableHead>
                        <TableHead>{t('Used')}</TableHead>
                        <TableHead>{t('Available')}</TableHead>
                        <TableHead>{t('Term')}</TableHead>
                        <TableHead>{t('Status')}</TableHead>
                        <TableHead className='text-right'>
                          {t('Actions')}
                        </TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {accountItems.map((account) => (
                        <TableRow key={account.user_id}>
                          <TableCell>
                            <div className='font-medium'>
                              {account.username}
                            </div>
                            <div className='text-muted-foreground text-xs'>
                              ID {account.user_id}
                            </div>
                          </TableCell>
                          <TableCell>
                            {formatCreditAmount(account.credit_limit)}
                          </TableCell>
                          <TableCell>
                            {formatCreditAmount(account.used_quota)}
                          </TableCell>
                          <TableCell>
                            {formatCreditAmount(account.available_quota)}
                          </TableCell>
                          <TableCell>
                            {account.payment_term_days ?? 0} {t('days')}
                          </TableCell>
                          <TableCell>
                            <Badge
                              variant={
                                account.status === 'active'
                                  ? 'default'
                                  : 'secondary'
                              }
                            >
                              {t(
                                account.status === 'active'
                                  ? 'Active'
                                  : 'Suspended'
                              )}
                            </Badge>
                          </TableCell>
                          <TableCell className='text-right'>
                            <Button
                              variant='outline'
                              size='sm'
                              onClick={() =>
                                void selectAccount(account.user_id)
                              }
                            >
                              {t('Open')}
                            </Button>
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                </div>
              )}
            </CardContent>
          </Card>
        </div>
      </SectionPageLayout.Content>

      <CollectionDialog
        statement={collection}
        onClose={() => setCollection(null)}
        onSaved={() => {
          setCollection(null)
          refresh()
          toast.success(t('Collection recorded'))
        }}
      />
      <BadDebtDialog
        statement={badDebt}
        onClose={() => setBadDebt(null)}
        onSaved={() => {
          setBadDebt(null)
          refresh()
          toast.success(t('Bad debt written off'))
        }}
      />
    </SectionPageLayout>
  )
}

function CreditAccountPanel(props: {
  account: CreditAccount
  statements: CreditStatement[]
  statementsLoading: boolean
  onSaved: (account: CreditAccount) => void
  onCollect: (statement: CreditStatement) => void
  onBadDebt: (statement: CreditStatement) => void
}) {
  const { t } = useTranslation()
  const [limit, setLimit] = useState(
    creditLimitToInput(props.account.credit_limit)
  )
  const [termDays, setTermDays] = useState(
    String(props.account.payment_term_days ?? 0)
  )
  const [status, setStatus] = useState(
    props.account.status === 'suspended' ? 'suspended' : 'active'
  )
  const [emailEnabled, setEmailEnabled] = useState(
    Boolean(props.account.email_enabled)
  )
  const mutation = useMutation({
    mutationFn: async () => {
      const creditLimit = creditLimitFromInput(limit)
      const paymentTermDays = Number(termDays)
      if (
        creditLimit === null ||
        !Number.isInteger(paymentTermDays) ||
        paymentTermDays < 0 ||
        paymentTermDays > 365
      ) {
        throw new Error(t('Enter a valid credit limit and payment term.'))
      }
      return requireServerSuccess(
        await saveCreditAccount(props.account.user_id, {
          credit_limit: creditLimit,
          payment_term_days: paymentTermDays,
          status,
          email_enabled: emailEnabled,
        })
      )
    },
    onSuccess: (response) => {
      if (response.data) props.onSaved(response.data)
      toast.success(t('Credit terms saved'))
    },
    onError: (error) =>
      handleServerError(error, t('Failed to save credit terms')),
  })

  let statementsContent: ReactNode
  if (props.statementsLoading) {
    statementsContent = (
      <p className='text-muted-foreground py-8 text-center'>
        {t('Loading...')}
      </p>
    )
  } else if (props.statements.length === 0) {
    statementsContent = (
      <EmptyState
        className='min-h-40'
        icon={CalendarDays}
        title={t('No statements')}
      />
    )
  } else {
    statementsContent = (
      <div className='overflow-x-auto'>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('Period')}</TableHead>
              <TableHead>{t('Amount')}</TableHead>
              <TableHead>{t('Remaining')}</TableHead>
              <TableHead>{t('Status')}</TableHead>
              <TableHead>{t('Due')}</TableHead>
              <TableHead>{t('Email')}</TableHead>
              <TableHead className='text-right'>{t('Actions')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {props.statements.map((statement) => (
              <StatementRow
                key={statement.id}
                statement={statement}
                onCollect={props.onCollect}
                onBadDebt={props.onBadDebt}
              />
            ))}
          </TableBody>
        </Table>
      </div>
    )
  }

  return (
    <div className='grid gap-4 xl:grid-cols-[minmax(0,0.8fr)_minmax(0,1.2fr)]'>
      <Card>
        <CardHeader>
          <CardTitle>{props.account.username}</CardTitle>
          <CardDescription>
            {t('User ID')}: {props.account.user_id}
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form
            className='grid gap-3 sm:grid-cols-2'
            onSubmit={(event) => {
              event.preventDefault()
              mutation.mutate()
            }}
          >
            <div className='space-y-1.5'>
              <Label htmlFor='credit-limit'>{t('Credit limit (CNY)')}</Label>
              <Input
                id='credit-limit'
                inputMode='decimal'
                value={limit}
                onChange={(event) => setLimit(event.target.value)}
              />
            </div>
            <div className='space-y-1.5'>
              <Label htmlFor='credit-term'>{t('Payment term (days)')}</Label>
              <Input
                id='credit-term'
                type='number'
                min={0}
                max={365}
                value={termDays}
                onChange={(event) => setTermDays(event.target.value)}
              />
            </div>
            <div className='space-y-1.5'>
              <Label>{t('Repayment day')}</Label>
              <div className='border-input bg-muted/40 text-muted-foreground flex h-8 items-center gap-2 rounded-lg border px-2.5 text-sm'>
                <CalendarDays className='size-4' />
                {t('Every month on day {{day}}', { day: CREDIT_REPAYMENT_DAY })}
              </div>
            </div>
            <div className='space-y-1.5'>
              <Label>{t('Credit status')}</Label>
              <Select
                value={status}
                onValueChange={(value) => setStatus(String(value))}
              >
                <SelectTrigger className='w-full'>
                  <SelectValue>
                    {t(status === 'active' ? 'Active' : 'Suspended')}
                  </SelectValue>
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value='active'>{t('Active')}</SelectItem>
                  <SelectItem value='suspended'>{t('Suspended')}</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <label className='flex items-center gap-2 sm:col-span-2'>
              <Checkbox
                checked={emailEnabled}
                onCheckedChange={(checked) => setEmailEnabled(checked === true)}
              />
              <span className='text-sm'>{t('Send statement emails')}</span>
            </label>
            <div className='flex items-center justify-between gap-3 sm:col-span-2'>
              <div className='text-muted-foreground text-xs'>
                {t('Used')}: {formatCreditAmount(props.account.used_quota)} ·{' '}
                {t('Available')}:{' '}
                {formatCreditAmount(props.account.available_quota)}
              </div>
              <Button type='submit' disabled={mutation.isPending}>
                <Check />
                {t('Save')}
              </Button>
            </div>
          </form>
        </CardContent>
      </Card>

      <Card className='min-w-0'>
        <CardHeader>
          <CardTitle>{t('Credit statements')}</CardTitle>
          <CardDescription>
            {t('Statements are issued on the first day of each month.')}
          </CardDescription>
        </CardHeader>
        <CardContent>{statementsContent}</CardContent>
      </Card>
    </div>
  )
}

function StatementRow(props: {
  statement: CreditStatement
  onCollect: (statement: CreditStatement) => void
  onBadDebt: (statement: CreditStatement) => void
}) {
  const { t } = useTranslation()
  const resend = useMutation({
    mutationFn: () => resendCreditStatementEmail(props.statement.id),
    onSuccess: () => toast.success(t('Statement email sent')),
    onError: (error) =>
      handleServerError(error, t('Failed to send statement email')),
  })
  return (
    <TableRow>
      <TableCell>{props.statement.period}</TableCell>
      <TableCell>{formatCreditAmount(props.statement.amount_quota)}</TableCell>
      <TableCell>
        {formatCreditAmount(props.statement.remaining_quota)}
      </TableCell>
      <TableCell>
        <Badge variant={statusVariant(props.statement.status)}>
          {t(statementStatusKey(props.statement.status))}
        </Badge>
      </TableCell>
      <TableCell className='whitespace-nowrap'>
        {timestamp(props.statement.due_at_ms)}
      </TableCell>
      <TableCell>
        <span className='inline-flex items-center gap-1'>
          <Mail className='size-3.5' />
          {t(emailStatusKey(props.statement.email_status))}
        </span>
      </TableCell>
      <TableCell>
        <div className='flex flex-wrap justify-end gap-1'>
          {isStatementOpen(props.statement) && (
            <Button
              variant='outline'
              size='sm'
              onClick={() => props.onCollect(props.statement)}
            >
              {t('Collect')}
            </Button>
          )}
          {isStatementOverdue(props.statement) && (
            <Button
              variant='destructive'
              size='sm'
              onClick={() => props.onBadDebt(props.statement)}
            >
              <ShieldAlert />
              {t('Write off')}
            </Button>
          )}
          {isStatementOpen(props.statement) && (
            <Button
              variant='ghost'
              size='sm'
              disabled={resend.isPending}
              onClick={() => resend.mutate()}
            >
              <Mail />
              {t('Resend')}
            </Button>
          )}
        </div>
      </TableCell>
    </TableRow>
  )
}

function CollectionDialog(props: {
  statement: CreditStatement | null
  onClose: () => void
  onSaved: () => void
}) {
  const { t } = useTranslation()
  const [amount, setAmount] = useState('')
  const [reference, setReference] = useState('')
  const mutation = useMutation({
    mutationFn: async () => {
      if (!props.statement || !reference.trim()) {
        throw new Error(t('Payment reference is required.'))
      }
      const amountQuota =
        amount.trim() === '' ? 0 : creditLimitFromInput(amount)
      if (amountQuota === null) throw new Error(t('Enter a valid amount.'))
      return requireServerSuccess(
        await registerCreditPayment(props.statement.id, {
          amount_quota: amountQuota,
          payment_reference: reference.trim(),
        })
      )
    },
    onSuccess: props.onSaved,
    onError: (error) =>
      handleServerError(error, t('Failed to record collection')),
  })
  return (
    <Dialog
      open={Boolean(props.statement)}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('Register collection')}
      description={t('Leave the amount blank to settle the remaining balance.')}
      contentClassName='sm:max-w-xl'
      footer={null}
    >
      {props.statement ? (
        <form
          className='space-y-4'
          onSubmit={(event) => {
            event.preventDefault()
            mutation.mutate()
          }}
        >
          <p className='text-muted-foreground text-sm'>
            {props.statement.username} · {props.statement.period} ·{' '}
            {formatCreditAmount(props.statement.remaining_quota)}
          </p>
          <div className='space-y-1.5'>
            <Label htmlFor='collection-amount'>
              {t('Collection amount (CNY)')}
            </Label>
            <Input
              id='collection-amount'
              inputMode='decimal'
              value={amount}
              onChange={(event) => setAmount(event.target.value)}
            />
          </div>
          <div className='space-y-1.5'>
            <Label htmlFor='payment-reference'>{t('Payment reference')}</Label>
            <Input
              id='payment-reference'
              required
              maxLength={191}
              value={reference}
              onChange={(event) => setReference(event.target.value)}
            />
          </div>
          <Button type='submit' disabled={mutation.isPending}>
            <Check />
            {t('Confirm collection')}
          </Button>
        </form>
      ) : null}
    </Dialog>
  )
}

function BadDebtDialog(props: {
  statement: CreditStatement | null
  onClose: () => void
  onSaved: () => void
}) {
  const { t } = useTranslation()
  const [reason, setReason] = useState('')
  const mutation = useMutation({
    mutationFn: async () => {
      if (!props.statement || !reason.trim()) {
        throw new Error(t('Bad debt reason is required.'))
      }
      return requireServerSuccess(
        await writeOffCreditStatement(props.statement.id, reason.trim())
      )
    },
    onSuccess: props.onSaved,
    onError: (error) =>
      handleServerError(error, t('Failed to write off bad debt')),
  })
  return (
    <Dialog
      open={Boolean(props.statement)}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('Write off bad debt')}
      description={t(
        'Only overdue statements can be written off. The credit line will be suspended.'
      )}
      contentClassName='sm:max-w-xl'
      footer={null}
    >
      {props.statement ? (
        <form
          className='space-y-4'
          onSubmit={(event) => {
            event.preventDefault()
            mutation.mutate()
          }}
        >
          <p className='text-muted-foreground text-sm'>
            {props.statement.username} · {props.statement.period} ·{' '}
            {formatCreditAmount(props.statement.remaining_quota)}
          </p>
          <div className='space-y-1.5'>
            <Label htmlFor='bad-debt-reason'>{t('Reason')}</Label>
            <Textarea
              id='bad-debt-reason'
              required
              rows={4}
              maxLength={512}
              value={reason}
              onChange={(event) => setReason(event.target.value)}
            />
          </div>
          <Button
            variant='destructive'
            type='submit'
            disabled={mutation.isPending}
          >
            <ShieldAlert />
            {t('Confirm write-off')}
          </Button>
        </form>
      ) : null}
    </Dialog>
  )
}
