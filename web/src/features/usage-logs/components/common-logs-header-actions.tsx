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
import { Download } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
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
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { handleServerError } from '@/lib/handle-server-error'

import { downloadUsageBill } from '../api'
import { buildApiParams } from '../lib/utils'
import { CompactDateTimeRangePicker } from './compact-date-time-range-picker'
import { useLogsViewScope } from './usage-logs-provider'

const route = getRouteApi('/_authenticated/usage-logs/$section')

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
  const [username, setUsername] = useState('')

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
      params.username = isRootView ? username.trim() || undefined : undefined
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
          {isRootView && (
            <div className='space-y-2'>
              <Label htmlFor='usage-bill-username'>{t('Username')}</Label>
              <Input
                id='usage-bill-username'
                value={username}
                placeholder={t('All users')}
                onChange={(event) => setUsername(event.target.value)}
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
