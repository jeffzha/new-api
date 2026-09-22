import type { Policy, PricingDraft } from "./types";

export const initialPolicy: Policy = {
  revision: 1,
  default_settlement_bps: 7500,
  default_child_cost_bps: 0,
  default_sales_bps: 10000,
  min_spread_bps: 500,
  sales_cap_bps: 30000,
  model_overrides: [],
};

export function formatCoefficient(bps: number): string {
  return (bps / 10000).toFixed(4);
}

export function parseCoefficient(value: string): number {
  if (!/^\d+(?:\.\d{1,4})?$/.test(value)) {
    throw new Error("Use a coefficient with up to four decimal places.");
  }
  const [whole, fraction = ""] = value.split(".");
  const bps = Number(whole) * 10000 + Number(fraction.padEnd(4, "0"));
  if (!Number.isSafeInteger(bps) || bps < 0 || bps > 100000) {
    throw new Error("Coefficients must be between 0 and 10.");
  }
  return bps;
}

export function policyToDraft(policy: Policy): PricingDraft {
  return {
    settlement: formatCoefficient(policy.default_settlement_bps),
    sales: formatCoefficient(policy.default_sales_bps),
    spread: formatCoefficient(policy.min_spread_bps),
    cap: formatCoefficient(policy.sales_cap_bps),
    overrides: (policy.model_overrides ?? []).map((row) => ({
      model: row.origin_model_name,
      settlement: row.settlement_bps == null ? "" : formatCoefficient(row.settlement_bps),
      sales: row.sales_bps == null ? "" : formatCoefficient(row.sales_bps),
    })),
  };
}

export function draftToPolicy(draft: PricingDraft, base: Policy, root: boolean): Policy {
  const policy: Policy = {
    revision: base.revision,
    default_settlement_bps: root ? parseCoefficient(draft.settlement) : base.default_settlement_bps,
    default_child_cost_bps: base.default_child_cost_bps ?? 0,
    default_sales_bps: parseCoefficient(draft.sales),
    min_spread_bps: root ? parseCoefficient(draft.spread) : base.min_spread_bps,
    sales_cap_bps: root ? parseCoefficient(draft.cap) : base.sales_cap_bps,
    model_overrides: [],
  };
  if (draft.overrides.length > 1000) throw new Error("At most 1000 model overrides are allowed.");
  const seen = new Set<string>();
  const baseOverrides = new Map(
    (base.model_overrides ?? []).map((row) => [row.origin_model_name, row]),
  );
  for (const row of draft.overrides) {
    if (
      !row.model ||
      row.model !== row.model.trim() ||
      [...row.model].length > 191 ||
      new TextEncoder().encode(row.model).length > 764
    ) {
      throw new Error("Use an exact model name without surrounding spaces, up to 191 characters.");
    }
    if (seen.has(row.model)) throw new Error("Each model can have only one override.");
    seen.add(row.model);
    policy.model_overrides!.push({
      origin_model_name: row.model,
      settlement_bps: root
        ? row.settlement === ""
          ? null
          : parseCoefficient(row.settlement)
        : (baseOverrides.get(row.model)?.settlement_bps ?? null),
      sales_bps: row.sales === "" ? null : parseCoefficient(row.sales),
    });
  }
  // Removing a sales override must never remove the administrator's settlement rule.
  if (!root) {
    for (const row of base.model_overrides ?? []) {
      if (!seen.has(row.origin_model_name) && row.settlement_bps != null) {
        policy.model_overrides!.push({ ...row, sales_bps: null });
      }
    }
  }
  const combinations = [
    [policy.default_settlement_bps, policy.default_sales_bps],
    ...policy.model_overrides!.map((row) => [
      row.settlement_bps ?? policy.default_settlement_bps,
      row.sales_bps ?? policy.default_sales_bps,
    ]),
  ];
  for (const [settlement, sales] of combinations) {
    if (
      policy.sales_cap_bps <= 0 ||
      policy.min_spread_bps > policy.sales_cap_bps ||
      sales <= 0 ||
      sales > policy.sales_cap_bps ||
      settlement > sales ||
      sales < settlement + policy.min_spread_bps
    ) {
      throw new Error(
        "Every sales coefficient must be positive, within the cap, and at least settlement plus spread.",
      );
    }
  }
  return policy;
}

export function pricingRequest(policy: Policy, root: boolean, reason: string) {
  if (!root) {
    return {
      expected_revision: policy.revision,
      default_sales_bps: policy.default_sales_bps,
      model_sales_overrides: (policy.model_overrides ?? []).map((row) => ({
        origin_model_name: row.origin_model_name,
        sales_bps: row.sales_bps ?? null,
      })),
      reason,
    };
  }
  return {
    expected_revision: policy.revision,
    default_settlement_bps: policy.default_settlement_bps,
    default_child_cost_bps: policy.default_child_cost_bps ?? 0,
    default_sales_bps: policy.default_sales_bps,
    min_spread_bps: policy.min_spread_bps,
    sales_cap_bps: policy.sales_cap_bps,
    model_overrides: policy.model_overrides ?? [],
    reason,
  };
}
