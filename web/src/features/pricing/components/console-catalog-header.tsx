/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { Boxes, Building2, Layers3 } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { SearchBar } from './search-bar'

export function ConsoleCatalogHeader(props: {
  models: number
  providers: number
  groups: number
  search: string
  onSearch: (value: string) => void
  onClear: () => void
}) {
  const { t } = useTranslation()
  return (
    <header className='console-catalog-header'>
      <div className='console-catalog-title'>
        <Boxes aria-hidden />
        <h1>{t('Model Square')}</h1>
      </div>
      <dl aria-label={t('Model catalog')} className='console-catalog-counts'>
        {[
          { label: t('Models'), value: props.models, icon: Boxes },
          { label: t('Providers'), value: props.providers, icon: Building2 },
          { label: t('Groups'), value: props.groups, icon: Layers3 },
        ].map((item) => (
          <div key={item.label}>
            <item.icon aria-hidden />
            <dt>{item.label}</dt>
            <dd>{item.value}</dd>
          </div>
        ))}
      </dl>
      <SearchBar
        value={props.search}
        onChange={props.onSearch}
        onClear={props.onClear}
        placeholder={t('Search model name, provider, endpoint, or tag...')}
        className='console-catalog-search'
      />
    </header>
  )
}
