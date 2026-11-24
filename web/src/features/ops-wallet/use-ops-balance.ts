import { useTranslation } from 'react-i18next'

import { toIntlLocale } from '@/i18n/languages'

import type { OPSBalance } from './types'

// Money is already denominated in the OPS response currency; product quota
// display preferences and product exchange rates must never convert it again.
export function useOPSBalance(balance?: OPSBalance, failed = false) {
  const { t, i18n } = useTranslation()
  const available =
    balance?.amount != null && Number.isFinite(Number(balance.amount))
  let hint = t('Balance unavailable')
  if (available) {
    hint = failed || balance?.stale ? t('Last known balance') : t('Balance')
  } else if (!failed && (!balance || balance.status === 'pending')) {
    hint = t('Reading balance')
  }
  return {
    text: available
      ? new Intl.NumberFormat(
          toIntlLocale(i18n.resolvedLanguage || i18n.language),
          {
            style: 'currency',
            currency: balance.currency || 'CNY',
            minimumFractionDigits: 2,
            maximumFractionDigits: 6,
          }
        ).format(Number(balance.amount))
      : '—',
    hint,
    quota: available ? balance?.quota : null,
  }
}
