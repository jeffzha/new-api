import { formatCoefficient, parseCoefficient } from "../pricing/policy";

export type ChildPricingModel = {
  origin_model_name: string;
  agency_cost_bps: number;
  settlement_bps?: number | null;
  child_cost_bps?: number | null;
  vendor_name?: string;
  vendor_icon?: string;
  channels?: { channel_id: number; channel_name: string }[];
};

export function childPricingOverridePayload(
  rows: ChildPricingModel[],
  values: Record<string, string>,
): { origin_model_name: string; settlement_bps: number }[] {
  return rows.flatMap((row) => {
    const value = values[row.origin_model_name] ?? (row.settlement_bps == null ? "" : formatCoefficient(row.settlement_bps));
    return value.trim() ? [{ origin_model_name: row.origin_model_name, settlement_bps: parseCoefficient(value) }] : [];
  });
}
