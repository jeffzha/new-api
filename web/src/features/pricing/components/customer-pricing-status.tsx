import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'

export function CustomerPricingNotice() {
  const { t } = useTranslation()
  return (
    <Alert>
      <AlertDescription>
        {t(
          'Prices include your agency sales policy. Group selection changes model availability, not your sales rate.'
        )}
      </AlertDescription>
    </Alert>
  )
}

export function PricingUnavailable(props: { onRetry: () => void }) {
  const { t } = useTranslation()
  return (
    <Alert variant='destructive'>
      <AlertDescription>
        {t('Pricing is temporarily unavailable. Please try again.')}
        <Button variant='outline' size='sm' onClick={props.onRetry}>
          {t('Retry')}
        </Button>
      </AlertDescription>
    </Alert>
  )
}
