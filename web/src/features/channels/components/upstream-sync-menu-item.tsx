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
import { useMutation } from '@tanstack/react-query'
import { RefreshCw } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { DropdownMenuItem } from '@/components/ui/dropdown-menu'
import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'

export function UpstreamSyncMenuItem(props: { disabled: boolean }) {
  const { t } = useTranslation()
  const mutation = useMutation({
    mutationFn: async () =>
      requireServerSuccess(
        (await api.post('/api/channel/upstreams/sync')).data
      ),
    onSuccess: () =>
      toast.success(t('Upstream registration will run in the background')),
  })

  return (
    <DropdownMenuItem
      disabled={props.disabled || mutation.isPending}
      aria-busy={mutation.isPending}
      onSelect={(event) => {
        event.preventDefault()
        if (!props.disabled && !mutation.isPending) mutation.mutate()
      }}
    >
      <RefreshCw aria-hidden='true' />
      {t(mutation.isPending ? 'Submitting...' : 'Register upstreams with OPS')}
    </DropdownMenuItem>
  )
}
