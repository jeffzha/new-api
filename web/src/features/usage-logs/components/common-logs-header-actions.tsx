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

import { Button } from '@/components/ui/button'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { handleServerError } from '@/lib/handle-server-error'

import { downloadUsageBill } from '../api'
import { buildApiParams } from '../lib/utils'
import { useLogsViewScope } from './usage-logs-provider'

const route = getRouteApi('/_authenticated/usage-logs/$section')

/**
 * Page-header action for downloading a complete consume bill using the
 * currently applied usage-log filters.
 */
export function CommonLogsHeaderActions() {
  const { t } = useTranslation()
  const { isAdminView: isAdmin } = useLogsViewScope()
  const searchParams = route.useSearch()
  const [downloading, setDownloading] = useState(false)

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
      const blob = await downloadUsageBill(params, isAdmin)
      const url = URL.createObjectURL(blob)
      const anchor = document.createElement('a')
      anchor.href = url
      anchor.download = 'usage-bill.csv'
      try {
        anchor.click()
      } finally {
        URL.revokeObjectURL(url)
      }
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
              variant='ghost'
              size='icon'
              onClick={handleDownload}
              disabled={downloading}
              aria-label={t('Download usage bill')}
              className='text-muted-foreground hover:text-foreground size-7'
            />
          }
        >
          <Download />
        </TooltipTrigger>
        <TooltipContent>{t('Download usage bill')}</TooltipContent>
      </Tooltip>
    </div>
  )
}
