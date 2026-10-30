/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { useInfiniteQuery, useQuery } from '@tanstack/react-query'
import { Check } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import {
  Combobox,
  ComboboxContent,
  ComboboxInput,
  ComboboxItem,
  ComboboxList,
} from '@/components/ui/combobox'
import { Label } from '@/components/ui/label'
import { searchUsers } from '@/features/users/api'
import type { User } from '@/features/users/types'
import { useDebounce } from '@/hooks/use-debounce'
import { requireServerSuccess } from '@/lib/server-error-message'

import { getCreditAccount } from '../api'
import { useCreditTerms } from '../hooks/use-credit-terms'
import type { CreditAccount } from '../types'
import { CreditTermsFields } from './credit-terms-form'

export function AddCreditDialog(props: {
  onClose: () => void
  onSaved: (account: CreditAccount) => void
}) {
  const { t } = useTranslation()
  const [selected, setSelected] = useState<User | null>(null)
  const [search, setSearch] = useState('')
  const [open, setOpen] = useState(false)
  const keyword = useDebounce(search.trim(), 300)
  const searching = keyword !== search.trim()
  const users = useInfiniteQuery({
    queryKey: ['credit', 'user-options', keyword],
    enabled: !searching,
    initialPageParam: 1,
    queryFn: async ({ pageParam }) =>
      requireServerSuccess(
        await searchUsers({
          keyword,
          p: pageParam,
          page_size: 20,
          sort_by: 'id',
          sort_order: 'asc',
        })
      ).data ?? { items: [], total: 0, page: pageParam, page_size: 20 },
    getNextPageParam: (last) =>
      last.page * last.page_size < last.total ? last.page + 1 : undefined,
  })
  const userItems = searching
    ? []
    : (users.data?.pages
        .flatMap((page) => page.items)
        .filter((user) => !user.DeletedAt) ?? [])
  const account = useQuery({
    queryKey: ['credit', 'new-account', selected?.id],
    enabled: Boolean(selected),
    queryFn: async () => {
      if (!selected) throw new Error(t('Failed to load credit account'))
      const result = requireServerSuccess(
        await getCreditAccount(String(selected.id))
      )
      if (!result.data) throw new Error(t('Failed to load credit account'))
      return result.data
    },
  })
  const editor = useCreditTerms(account.data, props.onSaved)
  const canSave = Boolean(
    account.data &&
    !account.data.configured &&
    !account.isFetching &&
    !editor.mutation.isPending
  )
  return (
    <Dialog
      open
      onOpenChange={(value) => {
        if (!value && !editor.mutation.isPending) props.onClose()
      }}
      title={t('Add credit line')}
      contentClassName='sm:max-w-xl'
      footer={
        <>
          <Button
            variant='outline'
            disabled={editor.mutation.isPending}
            onClick={props.onClose}
          >
            {t('Cancel')}
          </Button>
          <Button type='submit' form='add-credit-form' disabled={!canSave}>
            <Check />
            {t('Save')}
          </Button>
        </>
      }
    >
      <div className='space-y-5'>
        <div className='space-y-1.5'>
          <Label htmlFor='credit-customer'>{t('Customer')}</Label>
          <Combobox
            items={userItems}
            value={selected}
            open={open}
            onOpenChange={setOpen}
            filter={null}
            inputValue={open ? search : (selected?.username ?? '')}
            onInputValueChange={(value, details) => {
              if (details.reason === 'input-change') setSearch(value)
            }}
            onValueChange={(value) => {
              setSelected(value)
              editor.form.clearErrors()
              setSearch('')
            }}
            isItemEqualToValue={(a, b) => a.id === b.id}
            itemToStringLabel={(item) => item.username}
            disabled={editor.mutation.isPending}
          >
            <ComboboxInput
              id='credit-customer'
              aria-label={t('Customer')}
              placeholder={t('Search users by account, name, email or ID')}
              className='w-full'
            />
            <ComboboxContent>
              <ComboboxList
                aria-label={t('Users')}
                onScroll={(event) => {
                  const list = event.currentTarget
                  if (
                    list.scrollHeight - list.scrollTop - list.clientHeight <
                      48 &&
                    users.hasNextPage &&
                    !users.isFetching &&
                    !searching &&
                    !users.isError
                  ) {
                    void users.fetchNextPage()
                  }
                }}
              >
                {(item: User) => (
                  <ComboboxItem key={item.id} value={item}>
                    <span className='min-w-0 break-all'>
                      <span className='block font-medium'>{item.username}</span>
                      <span className='text-muted-foreground text-xs'>
                        ID {item.id}
                        {item.display_name ? ` · ${item.display_name}` : ''}
                        {item.email ? ` · ${item.email}` : ''}
                      </span>
                    </span>
                  </ComboboxItem>
                )}
              </ComboboxList>
              {(users.isPending || searching || users.isFetchingNextPage) && (
                <LoadingState size='sm' className='min-h-12 py-2' />
              )}
              {users.isError && (
                <div role='alert' className='p-2 text-sm'>
                  <p>{t('Failed to load users')}</p>
                  <Button
                    variant='outline'
                    size='sm'
                    onClick={() =>
                      users.isFetchNextPageError
                        ? void users.fetchNextPage()
                        : void users.refetch()
                    }
                  >
                    {t('Retry')}
                  </Button>
                </div>
              )}
              {!users.isPending &&
                !searching &&
                !users.isError &&
                userItems.length === 0 && (
                  <p
                    role='status'
                    className='text-muted-foreground p-3 text-sm'
                  >
                    {t('No results found')}
                  </p>
                )}
              {users.hasNextPage &&
                !users.isFetching &&
                !users.isError &&
                !searching && (
                  <Button
                    variant='ghost'
                    size='sm'
                    className='w-full'
                    onClick={() => void users.fetchNextPage()}
                  >
                    {t('Load more')}
                  </Button>
                )}
            </ComboboxContent>
          </Combobox>
        </div>
        {selected && account.isPending && <LoadingState size='sm' />}
        {selected && account.isError && (
          <ErrorState
            title={t('Failed to load credit account')}
            onRetry={() => void account.refetch()}
            className='min-h-32'
          />
        )}
        {account.data?.configured && (
          <p role='status' className='text-muted-foreground text-sm'>
            {t(
              'This customer already has a credit line. Open the configured account to edit it.'
            )}
          </p>
        )}
        {account.data && !account.data.configured && (
          <form id='add-credit-form' onSubmit={editor.submit}>
            <CreditTermsFields
              form={editor.form}
              id='new-credit'
              disabled={editor.mutation.isPending}
            />
          </form>
        )}
      </div>
    </Dialog>
  )
}
