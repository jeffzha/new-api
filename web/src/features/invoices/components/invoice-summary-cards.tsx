import { useTranslation } from 'react-i18next'

import {
  Card,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'

export function InvoiceSummaryCards(props: {
  summary?: { available: string; reserved: string; issued: string }
  loading?: boolean
}) {
  const { t } = useTranslation()
  const values = props.summary ?? {
    available: '0.00',
    reserved: '0.00',
    issued: '0.00',
  }
  const cards = [
    [t('Available to invoice'), values.available, 'text-primary'],
    [t('Under review'), values.reserved, 'text-warning'],
    [t('Already issued'), values.issued, 'text-success'],
  ]
  return (
    <div className='grid gap-3 sm:grid-cols-3'>
      {cards.map(([label, value, className]) => (
        <Card key={label} size='sm'>
          <CardHeader>
            <CardDescription>{label}</CardDescription>
            <CardTitle className={className}>
              {props.loading ? '—' : `¥${value}`}
            </CardTitle>
          </CardHeader>
        </Card>
      ))}
    </div>
  )
}
