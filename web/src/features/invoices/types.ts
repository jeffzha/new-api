export type InvoiceStatus =
  | 'pending_review'
  | 'rejected'
  | 'approved'
  | 'issued'
  | 'withdrawn'
  | 'void_pending'
  | 'voided'

export interface ApiResponse<T = unknown> {
  success: boolean
  message?: string
  data?: T
}

export interface InvoiceSummary {
  available_cents: number
  reserved_cents: number
  issued_cents: number
  available: string
  reserved: string
  issued: string
}

export interface InvoiceSource {
  topup_id: number
  trade_no: string
  paid_cents: number
  available_cents: number
  paid: string
  available: string
  currency_code: string
  paid_at: number
}

export interface InvoiceLine {
  id: number
  topup_id: number
  source_trade_no: string
  paid_cents: number
  allocated_cents: number
  paid: string
  allocated: string
  currency_code: string
}

export interface InvoiceEvent {
  id: number
  application_id: number
  actor_id: number
  actor_role: number
  action: string
  from_status: string
  to_status: string
  reason?: string
  created_at: number
}

export interface InvoiceApplication {
  id: number
  application_no: string
  user_id?: number
  status: InvoiceStatus
  currency_code: string
  total_cents: number
  total: string
  purchaser_name: string
  tax_number: string
  receiver_email: string
  bank_name?: string
  bank_account?: string
  address?: string
  phone?: string
  remark?: string
  review_reason?: string
  reviewer_id?: number
  reviewed_at?: number
  invoice_number?: string
  invoice_code?: string
  invoice_date?: number
  issuer_id?: number
  issued_at?: number
  email_status: string
  created_at: number
  updated_at: number
  has_document: boolean
  lines: InvoiceLine[]
  events?: InvoiceEvent[]
}

export interface Paginated<T> {
  page: number
  page_size: number
  total: number
  items: T[]
}

export interface InvoiceApplicationPayload {
  purchaser_name: string
  tax_number: string
  receiver_email: string
  bank_name?: string
  bank_account?: string
  address?: string
  phone?: string
  remark?: string
  lines: { topup_id: number; allocated_cents: number }[]
}
