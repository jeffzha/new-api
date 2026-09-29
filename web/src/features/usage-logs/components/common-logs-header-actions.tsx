/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { getRouteApi } from '@tanstack/react-router'
import { Download, Info } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Combobox,
  ComboboxContent,
  ComboboxInput,
  ComboboxItem,
  ComboboxList,
  useComboboxAnchor,
} from '@/components/ui/combobox'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { useDebounce } from '@/hooks/use-debounce'
import { handleServerError } from '@/lib/handle-server-error'

import { searchUsers } from '../../users/api'
import type { User } from '../../users/types'
import { downloadUsageBill } from '../api'
import { buildApiParams } from '../lib/utils'
import { CompactDateTimeRangePicker } from './compact-date-time-range-picker'
import { useLogsViewScope } from './usage-logs-provider'

const route = getRouteApi('/_authenticated/usage-logs/$section')

type BillUserOption = Pick<User, 'id' | 'username' | 'display_name'>

function BillUserSearch(props: {
  value: BillUserOption | null
  onValueChange: (value: BillUserOption | null) => void
}) {
  const { t } = useTranslation()
  const anchor = useComboboxAnchor()
  const [open, setOpen] = useState(false)
  const [search, setSearch] = useState('')
  const [users, setUsers] = useState<BillUserOption[]>([])
  const [status, setStatus] = useState<
    'idle' | 'loading' | 'success' | 'error'
  >('idle')
  const keyword = useDebounce(search.trim(), 250)
  const allUsers = useMemo<BillUserOption>(
    () => ({ id: 0, username: t('All users'), display_name: '' }),
    [t]
  )
  const selected = props.value ?? allUsers
  const options = useMemo(() => [allUsers, ...users], [allUsers, users])

  useEffect(() => {
    if (!open || !keyword) {
      setUsers([])
      setStatus('idle')
      return
    }

    let active = true
    setStatus('loading')
    searchUsers({ keyword, p: 1, page_size: 20 })
      .then((response) => {
        if (!active) return
        if (!response.success || !response.data) {
          setUsers([])
          setStatus('error')
          return
        }
        setUsers(
          response.data.items.map((user) => ({
            id: user.id,
            username: user.username,
            display_name: user.display_name,
          }))
        )
        setStatus('success')
      })
      .catch(() => {
        if (!active) return
        setUsers([])
        setStatus('error')
      })

    return () => {
      active = false
    }
  }, [keyword, open])

  return (
    <Combobox<BillUserOption>
      items={options}
      value={selected}
      open={open}
      inputValue={open ? search : selected.username}
      onInputValueChange={(value, details) => {
        if (details.reason === 'input-change') setSearch(value)
      }}
      onOpenChange={(nextOpen) => {
        setOpen(nextOpen)
        setSearch('')
      }}
      onValueChange={(option) => {
        props.onValueChange(option?.id ? option : null)
        setOpen(false)
        setSearch('')
      }}
      filter={() => true}
      isItemEqualToValue={(item, value) => item.id === value.id}
    >
      <div ref={anchor}>
        <ComboboxInput
          id='usage-bill-username'
          aria-label={t('Username')}
          placeholder={t('Enter a username to search.')}
          triggerAriaLabel={t('Search users')}
          className='h-9 w-full'
        />
      </div>
      <ComboboxContent anchor={anchor}>
        <ComboboxList>
          <ComboboxItem value={allUsers}>{t('All users')}</ComboboxItem>
          {users.map((user) => (
            <ComboboxItem key={user.id} value={user}>
              <span className='min-w-0 flex-1'>
                <span className='block truncate font-medium'>
                  {user.username}
                </span>
                {user.display_name && (
                  <span className='text-muted-foreground block truncate text-xs'>
                    {user.display_name}
                  </span>
                )}
              </span>
              <span className='text-muted-foreground shrink-0 text-xs'>
                ID {user.id}
              </span>
            </ComboboxItem>
          ))}
        </ComboboxList>
        {status === 'idle' && (
          <p className='text-muted-foreground px-3 py-2 text-xs'>
            {t('Enter a username to search.')}
          </p>
        )}
        {status === 'loading' && (
          <p className='text-muted-foreground px-3 py-2 text-xs'>
            {t('Loading...')}
          </p>
        )}
        {status === 'success' && users.length === 0 && (
          <p className='text-muted-foreground px-3 py-2 text-xs'>
            {t('No matching users found.')}
          </p>
        )}
        {status === 'error' && (
          <p className='text-destructive px-3 py-2 text-xs'>
            {t('Failed to search users')}
          </p>
        )}
      </ComboboxContent>
    </Combobox>
  )
}

/**
 * Page-header action for downloading a complete consume bill using the
 * currently applied usage-log filters.
 */
export function CommonLogsHeaderActions() {
  const { t } = useTranslation()
  const { isAdminView: isAdmin, isRootView } = useLogsViewScope()
  const searchParams = route.useSearch()
  const [open, setOpen] = useState(false)
  const [downloading, setDownloading] = useState(false)
  const [format, setFormat] = useState<'csv' | 'xlsx' | 'pdf' | 'docx'>('xlsx')
  const [start, setStart] = useState<Date>()
  const [end, setEnd] = useState<Date>()
  const [selectedUser, setSelectedUser] = useState<BillUserOption | null>(null)

  const handleDownload = async () => {
    setDownloading(true)
    try {
      const params = buildApiParams({
        page: 1,
        pageSize: 1,
        searchParams,
        columnFilters: [],
        isAdmin,
      })
      params.start_timestamp = start
        ? Math.floor(start.getTime() / 1000)
        : undefined
      params.end_timestamp = end ? Math.floor(end.getTime() / 1000) : undefined
      params.username = isRootView ? selectedUser?.username : undefined
      const blob = await downloadUsageBill(params, isAdmin, format)
      const url = URL.createObjectURL(blob)
      const anchor = document.createElement('a')
      anchor.href = url
      anchor.download = `usage-bill.${format}`
      try {
        anchor.click()
      } finally {
        URL.revokeObjectURL(url)
      }
      setOpen(false)
    } catch (error) {
      handleServerError(error, t('Failed to download usage bill'))
    } finally {
      setDownloading(false)
    }
  }

  return (
    <div className='flex flex-wrap items-center gap-2'>
      <Tooltip>
        <TooltipTrigger
          render={
            <Button
              variant='outline'
              size='sm'
              onClick={() => setOpen(true)}
              disabled={downloading}
              aria-label={t('Download usage bill')}
              className='h-8 gap-2 px-3'
            />
          }
        >
          <Download className='size-4' />
          <span>{t('Download bill')}</span>
        </TooltipTrigger>
        <TooltipContent>{t('Download usage bill')}</TooltipContent>
      </Tooltip>
      <Dialog
        open={open}
        onOpenChange={setOpen}
        title={t('Download bill')}
        description={t(
          'Choose a date range or leave it blank to export all matching records.'
        )}
        contentClassName='sm:max-w-lg'
        footer={
          <>
            <Button variant='outline' onClick={() => setOpen(false)}>
              {t('Cancel')}
            </Button>
            <Button onClick={handleDownload} disabled={downloading}>
              <Download className='size-4' />
              {downloading ? t('Downloading...') : t('Download bill')}
            </Button>
          </>
        }
      >
        <div className='space-y-5'>
          <Alert>
            <Info className='size-4' />
            <AlertDescription className='text-xs'>
              {t(
                "Actual consumption is the customer's final charge after agency sales pricing, customer-specific pricing, and applicable discounts. Exported files show whether each charge used an agency discount and the discount amount."
              )}
            </AlertDescription>
          </Alert>
          {isRootView && (
            <div className='space-y-2'>
              <Label htmlFor='usage-bill-username'>{t('Username')}</Label>
              <BillUserSearch
                value={selectedUser}
                onValueChange={setSelectedUser}
              />
            </div>
          )}
          <div className='space-y-2'>
            <Label>{t('Bill period')}</Label>
            <CompactDateTimeRangePicker
              start={start}
              end={end}
              emptyLabel={t('All time')}
              onChange={(range) => {
                setStart(range.start)
                setEnd(range.end)
              }}
            />
            <div className='flex items-center justify-between gap-3'>
              <p className='text-muted-foreground text-xs'>
                {!start && !end
                  ? t(
                      'No date selected. The bill will include all matching records.'
                    )
                  : t('The selected date range will be used for this bill.')}
              </p>
              {(start || end) && (
                <Button
                  type='button'
                  variant='ghost'
                  size='sm'
                  className='h-7 shrink-0 px-2 text-xs'
                  onClick={() => {
                    setStart(undefined)
                    setEnd(undefined)
                  }}
                >
                  {t('All time')}
                </Button>
              )}
            </div>
          </div>
          <div className='space-y-2'>
            <Label htmlFor='usage-bill-format'>{t('File format')}</Label>
            <Select
              value={format}
              onValueChange={(value) => {
                if (value) setFormat(value as typeof format)
              }}
            >
              <SelectTrigger
                id='usage-bill-format'
                aria-label={t('File format')}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value='xlsx'>
                  {t('Excel workbook (XLSX)')}
                </SelectItem>
                <SelectItem value='pdf'>{t('PDF document')}</SelectItem>
                <SelectItem value='csv'>{t('CSV file')}</SelectItem>
                <SelectItem value='docx'>
                  {t('Word document (DOCX)')}
                </SelectItem>
              </SelectContent>
            </Select>
          </div>
        </div>
      </Dialog>
    </div>
  )
}
