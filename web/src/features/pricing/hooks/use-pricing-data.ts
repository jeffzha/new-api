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
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useMemo } from 'react'

import { useStatus } from '@/hooks/use-status'
import { requireServerSuccess } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import { getPricing } from '../api'

export function usePricingData(enabled = true) {
  const { status } = useStatus()
  const userId = useAuthStore((state) => state.auth.user?.id ?? 0)
  const queryClient = useQueryClient()

  const { data, isLoading, error, refetch } = useQuery({
    queryKey: ['pricing', userId],
    queryFn: async () => requireServerSuccess(await getPricing()),
    staleTime: 5 * 60 * 1000,
    enabled,
  })

  // Ensure rates never reach zero to prevent division errors
  const priceRate = useMemo(
    () => Math.max((status?.price as number) ?? 1, 0.001),
    [status?.price]
  )
  const usdExchangeRate = useMemo(
    () => Math.max((status?.usd_exchange_rate as number) ?? priceRate, 0.001),
    [status?.usd_exchange_rate, priceRate]
  )

  const effectiveData =
    data ?? (userId === 0 ? queryClient.getQueryData<typeof data>(['pricing']) : undefined)

  const models = useMemo(() => {
    if (error || !effectiveData?.data || !effectiveData?.vendors) return []

    const vendorMap = new Map(effectiveData.vendors.map((v) => [v.id, v]))

    return effectiveData.data.map((model) => {
      const vendor = model.vendor_id
        ? vendorMap.get(model.vendor_id)
        : undefined
      return {
        ...model,
        key: model.model_name,
        vendor_name: vendor?.name,
        vendor_icon: vendor?.icon,
        vendor_description: vendor?.description,
        group_ratio: effectiveData.group_ratio,
      }
    })
  }, [effectiveData, error])
  const agencyPricing = effectiveData?.pricing_scope === 'agency'

  return {
    models,
    vendors: effectiveData?.vendors ?? [],
    groupRatio: effectiveData?.group_ratio ?? {},
    agencyPricing,
    usableGroup: effectiveData?.usable_group ?? {},
    endpointMap: effectiveData?.supported_endpoint ?? {},
    autoGroups: effectiveData?.auto_groups ?? [],
    isLoading,
    error,
    refetch,
    priceRate,
    usdExchangeRate,
  }
}
