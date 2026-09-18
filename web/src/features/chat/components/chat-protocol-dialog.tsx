/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { ExternalLink } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'

type ChatProtocolDialogProps = {
  open: boolean
  name: string
  url: string
  onOpenChange: (open: boolean) => void
}

export function ChatProtocolDialog({
  open,
  name,
  url,
  onOpenChange,
}: ChatProtocolDialogProps) {
  const { t } = useTranslation()

  const openApplication = () => {
    if (typeof window !== 'undefined' && url) {
      window.location.assign(url)
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={name}
      contentClassName='sm:max-w-xl'
      footer={
        <div className='flex w-full flex-wrap justify-end gap-2'>
          <Button variant='outline' onClick={() => onOpenChange(false)}>
            {t('Cancel')}
          </Button>
          <CopyButton
            value={url}
            variant='outline'
            size='default'
            tooltip={t('Copy URL')}
            successTooltip={t('Copied!')}
          >
            {t('Copy URL')}
          </CopyButton>
          <Button onClick={openApplication}>
            <ExternalLink />
            {t('Open application')}
          </Button>
        </div>
      }
    >
      <Textarea
        value={url}
        readOnly
        rows={4}
        aria-label={t('Copy URL')}
        className='font-mono text-xs leading-5'
        onFocus={(event) => event.currentTarget.select()}
      />
    </Dialog>
  )
}
