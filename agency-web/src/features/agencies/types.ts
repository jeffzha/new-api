export type Agency = {
  id: number | string;
  display_name: string;
  operator_username?: string;
  status: string;
  version: number;
  price_revision: number;
  invite_code: string;
  invite_url: string;
  invite_qr_url?: string;
};

export type AgencyList = { items: Agency[]; total: number; meta?: { next_cursor?: string } };

export type PasswordDelivery = {
  agency?: Agency;
  agency_id?: string | number;
  invite_code?: string;
  invite_url?: string;
  delivery_id: string | number;
  delivery_operation_id: string;
  temporary_password?: string;
  temporary_password_expires_at?: number;
};
