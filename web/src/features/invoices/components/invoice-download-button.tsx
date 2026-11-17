import { useMutation } from '@tanstack/react-query'
import { FileDown } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { handleServerError } from '@/lib/handle-server-error'

import { getInvoicePDF } from '../api'

export function InvoiceDownloadButton(props: { id: number; admin?: boolean }) {
  const { t } = useTranslation()
  const download = useMutation({
    mutationFn: () => getInvoicePDF(props.id, props.admin),
    onSuccess: (blob) => {
      const url = URL.createObjectURL(blob)
      const anchor = document.createElement('a')
      anchor.href = url
      anchor.download = `invoice-${props.id}.pdf`
      document.body.append(anchor)
      anchor.click()
      anchor.remove()
      // Keep the URL alive until the browser has started reading the file.
      window.setTimeout(() => URL.revokeObjectURL(url), 1000)
    },
    onError: (error) =>
      handleServerError(error, t('Failed to download invoice PDF')),
  })
  return (
    <Button
      size='sm'
      variant='outline'
      disabled={download.isPending}
      onClick={() => download.mutate()}
    >
      <FileDown className='size-4' />
      {t('Download PDF')}
    </Button>
  )
}
