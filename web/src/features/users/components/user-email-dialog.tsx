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
import { Loader2, Send } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { handleServerError } from '@/lib/handle-server-error'

import { sendUserEmail } from '../api'

interface UserEmailDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  userId: number
  username: string
  email?: string
}

export function UserEmailDialog({
  open,
  onOpenChange,
  userId,
  username,
  email,
}: UserEmailDialogProps) {
  const { t } = useTranslation()
  const [subject, setSubject] = useState('')
  const [message, setMessage] = useState('')
  const [sending, setSending] = useState(false)

  const handleOpenChange = (next: boolean) => {
    if (!next) {
      setSubject('')
      setMessage('')
    }
    onOpenChange(next)
  }

  const handleSend = async () => {
    if (!subject.trim() || !message.trim()) return
    setSending(true)
    try {
      const result = await sendUserEmail(userId, {
        subject: subject.trim(),
        message: message.trim(),
      })
      if (!result.success) {
        handleServerError(result, t('Failed to send email'))
        return
      }
      toast.success(t('Email sent successfully'))
      handleOpenChange(false)
    } catch (error) {
      handleServerError(error, t('Failed to send email'))
    } finally {
      setSending(false)
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={handleOpenChange}
      title={t('Send email')}
      description={t('Send an email to {{username}}', { username })}
      contentHeight='auto'
      bodyClassName='space-y-4'
      footer={
        <>
          <Button
            type='button'
            variant='outline'
            onClick={() => handleOpenChange(false)}
            disabled={sending}
          >
            {t('Cancel')}
          </Button>
          <Button
            type='button'
            onClick={handleSend}
            disabled={sending || !subject.trim() || !message.trim()}
          >
            {sending ? <Loader2 className='animate-spin' /> : <Send />}
            {t('Send email')}
          </Button>
        </>
      }
    >
      <div className='space-y-4'>
        <div className='space-y-2'>
          <Label htmlFor='user-email-recipient'>{t('Recipient')}</Label>
          <Input id='user-email-recipient' value={email || username} disabled />
        </div>
        <div className='space-y-2'>
          <Label htmlFor='user-email-subject'>{t('Email subject')}</Label>
          <Input
            id='user-email-subject'
            value={subject}
            onChange={(event) => setSubject(event.target.value)}
            maxLength={120}
            placeholder={t('Enter email subject')}
          />
        </div>
        <div className='space-y-2'>
          <Label htmlFor='user-email-message'>{t('Email message')}</Label>
          <Textarea
            id='user-email-message'
            value={message}
            onChange={(event) => setMessage(event.target.value)}
            maxLength={5000}
            rows={8}
            placeholder={t('Enter email message')}
          />
        </div>
      </div>
    </Dialog>
  )
}
