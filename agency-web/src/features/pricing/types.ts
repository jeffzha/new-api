export type ModelOverride = {
  origin_model_name: string;
  settlement_bps?: number | null;
  sales_bps?: number | null;
};

export type Policy = {
  agency_id?: string | number;
  revision: number;
  default_settlement_bps: number;
  default_sales_bps: number;
  min_spread_bps: number;
  sales_cap_bps: number;
  model_overrides?: ModelOverride[] | null;
};

export type PricingDraft = {
  settlement: string;
  sales: string;
  spread: string;
  cap: string;
  overrides: { model: string; settlement: string; sales: string }[];
};

export type PricingHistory = {
  policy_version_id: number | string;
  revision: number;
  policy: Policy;
  created_at_ms: number;
  reason: string;
};

export type PricePreview = {
  standard_quota: number;
  customer_quota: number;
  settlement_quota: number;
  commission_quota: number;
  model_previews?: {
    origin_model_name: string;
    settlement_bps: number;
    sales_bps: number;
    customer_quota: number;
    settlement_quota: number;
    commission_quota: number;
  }[];
};

export type PlatformPriceRow = {
  origin_model_name: string;
  channel_names: string[];
  channel_costs: {
    channel_id: number;
    channel_name: string;
    available: boolean;
    platform_cost_bps: number | null;
  }[];
  platform_cost_bps: number | null;
  agency_cost_bps: number | null;
  default_sales_bps: number | null;
};

export type PlatformPricing = {
  revision: number;
  items: PlatformPriceRow[];
  refreshed_at_ms: number;
};

export type ModelSaleRow = {
  origin_model_name: string;
  agency_cost_bps: number;
  platform_default_sales_bps: number;
  sales_bps: number;
  override_sales_bps: number | null;
};

export type ModelSales = {
  agency_id: string | number;
  agency_name: string;
  revision: number;
  platform_revision: number;
  default_sales_bps: number;
  items: ModelSaleRow[];
};
