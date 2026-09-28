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
import { useCallback, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { PublicLayout } from '@/components/layout'
import { PageTransition } from '@/components/page-transition'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useThemeCustomization } from '@/context/theme-customization-provider'
import { usesConsoleWorkspace } from '@/lib/theme-customization'

import {
  LoadingSkeleton,
  EmptyState,
  SearchBar,
  PricingTable,
  PricingSidebar,
  PricingToolbar,
  ModelCardGrid,
  ModelDetailsDrawer,
} from './components'
import {
  CustomerPricingNotice,
  PricingUnavailable,
} from './components/customer-pricing-status'
import { EXCLUDED_GROUPS, VIEW_MODES } from './constants'
import { useFilters } from './hooks/use-filters'
import { usePricingData } from './hooks/use-pricing-data'

export function Pricing() {
  const { t } = useTranslation()
  const { customization } = useThemeCustomization()
  const signal = usesConsoleWorkspace(customization.preset)
  const [selectedModelName, setSelectedModelName] = useState<string | null>(
    null
  )

  const {
    models,
    vendors,
    groupRatio,
    agencyPricing,
    usableGroup,
    endpointMap,
    autoGroups,
    isLoading,
    error,
    refetch,
    priceRate,
    usdExchangeRate,
  } = usePricingData()

  const availableGroups = useMemo(
    () =>
      Object.keys(usableGroup || {}).filter(
        (group) => !EXCLUDED_GROUPS.includes(group)
      ),
    [usableGroup]
  )

  // The catalog is public. Never expose internal-only routing groups here.
  const publicModels = useMemo(() => {
    const visibleGroups = new Set(availableGroups)
    return (models || []).flatMap((model) => {
      const enableGroups = (model.enable_groups || []).filter((group) =>
        visibleGroups.has(group)
      )
      return enableGroups.length > 0
        ? [{ ...model, enable_groups: enableGroups }]
        : []
    })
  }, [availableGroups, models])

  const {
    searchInput,
    sortBy,
    vendorFilter,
    groupFilter,
    quotaTypeFilter,
    endpointTypeFilter,
    tagFilter,
    tokenUnit,
    viewMode,
    showRechargePrice,
    setSearchInput,
    setSortBy,
    setVendorFilter,
    setGroupFilter,
    setQuotaTypeFilter,
    setEndpointTypeFilter,
    setTagFilter,
    setTokenUnit,
    setViewMode,
    setShowRechargePrice,
    filteredModels,
    hasActiveFilters,
    activeFilterCount,
    availableTags,
    clearFilters,
    clearSearch,
  } = useFilters(publicModels)

  const handleModelClick = useCallback((modelName: string) => {
    setSelectedModelName(modelName)
  }, [])

  const selectedModel = useMemo(
    () =>
      selectedModelName
        ? publicModels.find(
            (model) => model.model_name === selectedModelName
          ) || null
        : null,
    [publicModels, selectedModelName]
  )

  const handleClearAll = useCallback(() => {
    clearFilters()
    clearSearch()
  }, [clearFilters, clearSearch])

  const renderPricingContent = () => {
    if (error) {
      return (
        <PricingUnavailable
          onRetry={() => {
            void refetch()
          }}
        />
      )
    }
    if (filteredModels.length === 0) {
      return (
        <EmptyState
          searchQuery={searchInput}
          hasActiveFilters={hasActiveFilters}
          onClearFilters={handleClearAll}
        />
      )
    }

    if (viewMode === VIEW_MODES.CARD) {
      return (
        <ModelCardGrid
          models={filteredModels}
          onModelClick={handleModelClick}
          priceRate={priceRate}
          usdExchangeRate={usdExchangeRate}
          tokenUnit={tokenUnit}
          showRechargePrice={showRechargePrice}
          selectedGroup={groupFilter}
        />
      )
    }

    return (
      <PricingTable
        models={filteredModels}
        priceRate={priceRate}
        usdExchangeRate={usdExchangeRate}
        tokenUnit={tokenUnit}
        showRechargePrice={showRechargePrice}
        selectedGroup={groupFilter}
        onModelClick={handleModelClick}
      />
    )
  }

  if (isLoading) {
    return (
      <PublicLayout showMainContainer={false}>
        <div className='mx-auto w-full max-w-[1800px] px-3 pt-16 pb-8 sm:px-6 sm:pt-20 sm:pb-10 xl:px-8'>
          <LoadingSkeleton viewMode={viewMode} />
        </div>
      </PublicLayout>
    )
  }

  return (
    <PublicLayout showMainContainer={false}>
      <div className='signal-marketplace relative'>
        {!signal && (
          <div
            aria-hidden
            className='pointer-events-none absolute inset-x-0 top-0 h-[600px] opacity-20 dark:opacity-[0.10]'
            style={{
              background: [
                'radial-gradient(ellipse 60% 50% at 20% 20%, oklch(0.72 0.18 250 / 80%) 0%, transparent 70%)',
                'radial-gradient(ellipse 50% 40% at 80% 15%, oklch(0.65 0.15 200 / 60%) 0%, transparent 70%)',
                'radial-gradient(ellipse 40% 35% at 50% 70%, oklch(0.70 0.12 280 / 40%) 0%, transparent 70%)',
              ].join(', '),
              maskImage:
                'linear-gradient(to bottom, black 40%, transparent 100%)',
              WebkitMaskImage:
                'linear-gradient(to bottom, black 40%, transparent 100%)',
            }}
          />
        )}
        <PageTransition className='relative mx-auto w-full max-w-[1800px] px-3 pt-16 pb-8 sm:px-6 sm:pt-20 sm:pb-10 xl:px-8'>
          <header className='signal-marketplace-hero mx-auto mb-5 max-w-3xl pt-5 text-center sm:mb-10 sm:pt-10'>
            <h1 className='text-3xl leading-tight font-bold'>
              {t('Model Square')}
            </h1>
            <p className='text-muted-foreground/80 mt-3 text-sm sm:mt-4 sm:text-base'>
              {t('This site currently has {{count}} models enabled', {
                count: models?.length || 0,
              })}
            </p>
            {!signal && (
              <p className='text-muted-foreground/60 mx-auto mt-2 max-w-2xl text-xs leading-relaxed sm:text-sm'>
                {t(
                  'Discover curated AI models, compare pricing and capabilities, and choose the right model for every scenario.'
                )}
              </p>
            )}
            {signal && (
              <dl className='mt-4 flex flex-wrap gap-8 border-y py-4 text-sm'>
                <div>
                  <dt className='text-muted-foreground'>{t('Models')}</dt>
                  <dd className='mt-1 text-xl font-semibold'>
                    {publicModels.length}
                  </dd>
                </div>
                <div>
                  <dt className='text-muted-foreground'>{t('Providers')}</dt>
                  <dd className='mt-1 text-xl font-semibold'>
                    {
                      new Set(
                        publicModels
                          .map((model) => model.vendor_id)
                          .filter(Boolean)
                      ).size
                    }
                  </dd>
                </div>
                <div>
                  <dt className='text-muted-foreground'>{t('Groups')}</dt>
                  <dd className='mt-1 text-xl font-semibold'>
                    {availableGroups.length}
                  </dd>
                </div>
              </dl>
            )}
            <SearchBar
              value={searchInput}
              onChange={setSearchInput}
              onClear={clearSearch}
              placeholder={t(
                'Search model name, provider, endpoint, or tag...'
              )}
              className='mx-auto mt-4 max-w-2xl sm:mt-6'
            />
          </header>

          {signal && (
            <Tabs
              value={vendorFilter}
              onValueChange={(value) => setVendorFilter(String(value))}
              className='mb-5 min-w-0'
            >
              <TabsList
                variant='line'
                aria-label={t('Providers')}
                className='h-11 max-w-full justify-start overflow-x-auto'
              >
                <TabsTrigger value='all'>{t('All')}</TabsTrigger>
                {(vendors ?? [])
                  .filter((vendor) =>
                    publicModels.some((model) => model.vendor_id === vendor.id)
                  )
                  .map((vendor) => (
                    <TabsTrigger key={vendor.id} value={vendor.name}>
                      {vendor.name}
                    </TabsTrigger>
                  ))}
              </TabsList>
            </Tabs>
          )}
          <div
            className={
              signal
                ? 'grid min-w-0 gap-4'
                : 'signal-marketplace-grid grid gap-4 xl:grid-cols-[330px_minmax(0,1fr)]'
            }
          >
            {!signal && (
              <PricingSidebar
                quotaTypeFilter={quotaTypeFilter}
                endpointTypeFilter={endpointTypeFilter}
                vendorFilter={vendorFilter}
                groupFilter={groupFilter}
                tagFilter={tagFilter}
                onQuotaTypeChange={setQuotaTypeFilter}
                onEndpointTypeChange={setEndpointTypeFilter}
                onVendorChange={setVendorFilter}
                onGroupChange={setGroupFilter}
                onTagChange={setTagFilter}
                vendors={vendors || []}
                groups={availableGroups}
                groupRatios={agencyPricing ? undefined : groupRatio}
                tags={availableTags}
                models={models || []}
                hasActiveFilters={hasActiveFilters}
                onClearFilters={clearFilters}
                className='hover-scrollbar sticky top-4 hidden max-h-[calc(100dvh-2rem)] self-start overflow-y-auto xl:block'
              />
            )}

            <main className='min-w-0 space-y-4'>
              {agencyPricing && !error && (
                <CustomerPricingNotice models={publicModels} />
              )}
              <PricingToolbar
                alwaysShowFilters={signal}
                filteredCount={filteredModels.length}
                totalCount={models?.length}
                sortBy={sortBy}
                onSortChange={setSortBy}
                tokenUnit={tokenUnit}
                onTokenUnitChange={setTokenUnit}
                showRechargePrice={showRechargePrice}
                onRechargePriceChange={setShowRechargePrice}
                viewMode={viewMode}
                onViewModeChange={setViewMode}
                quotaTypeFilter={quotaTypeFilter}
                endpointTypeFilter={endpointTypeFilter}
                vendorFilter={vendorFilter}
                groupFilter={groupFilter}
                tagFilter={tagFilter}
                onQuotaTypeChange={setQuotaTypeFilter}
                onEndpointTypeChange={setEndpointTypeFilter}
                onVendorChange={setVendorFilter}
                onGroupChange={setGroupFilter}
                onTagChange={setTagFilter}
                vendors={vendors || []}
                groups={availableGroups}
                groupRatios={agencyPricing ? undefined : groupRatio}
                tags={availableTags}
                models={models || []}
                hasActiveFilters={hasActiveFilters}
                activeFilterCount={activeFilterCount}
                onClearFilters={clearFilters}
              />

              {renderPricingContent()}
            </main>
          </div>

          {selectedModel && (
            <ModelDetailsDrawer
              open={Boolean(selectedModel)}
              onOpenChange={(open) => {
                if (!open) setSelectedModelName(null)
              }}
              model={selectedModel}
              pricingModels={publicModels}
              groupRatio={groupRatio || {}}
              usableGroup={usableGroup || {}}
              endpointMap={
                (endpointMap as Record<
                  string,
                  { path?: string; method?: string }
                >) || {}
              }
              autoGroups={autoGroups || []}
              priceRate={priceRate ?? 1}
              usdExchangeRate={usdExchangeRate ?? 1}
              tokenUnit={tokenUnit}
              showRechargePrice={showRechargePrice}
            />
          )}
        </PageTransition>
      </div>
    </PublicLayout>
  )
}
