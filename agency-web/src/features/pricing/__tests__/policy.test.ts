import { describe, expect, test } from "bun:test";
import {
  draftToPolicy,
  initialPolicy,
  parseCoefficient,
  policyToDraft,
  pricingRequest,
} from "../policy";
import type { Policy } from "../types";

describe("pricing request contracts", () => {
  test("new agency drafts submit the standard price while existing discounts remain unchanged", () => {
    const newDraft = policyToDraft(initialPolicy);
    expect(newDraft.sales).toBe("1.0000");
    expect(
      pricingRequest(draftToPolicy(newDraft, initialPolicy, true), true, "Create agency")
        .default_sales_bps,
    ).toBe(10000);

    const existing: Policy = { ...initialPolicy, revision: 7, default_sales_bps: 9000 };
    const existingDraft = policyToDraft(existing);
    expect(existingDraft.sales).toBe("0.9000");
    expect(pricingRequest(draftToPolicy(existingDraft, existing, true), true, "")).toMatchObject({
      expected_revision: 7,
      default_sales_bps: 9000,
    });
  });

  test("operator publication sends only the strict sales DTO and preserves the revision", () => {
    const base: Policy = {
      ...initialPolicy,
      revision: 9,
      model_overrides: [{ origin_model_name: "Model-A", settlement_bps: 8000, sales_bps: 9500 }],
    };
    const draft = policyToDraft(base);
    draft.sales = "1.1001";
    const request = pricingRequest(draftToPolicy(draft, base, false), false, "Updated sales price");
    expect(request).toEqual({
      expected_revision: 9,
      default_sales_bps: 11001,
      model_sales_overrides: [{ origin_model_name: "Model-A", sales_bps: 9500 }],
      reason: "Updated sales price",
    });
  });

  test("operator deleting a sales override retains the root settlement and restores inherited sales", () => {
    const base: Policy = {
      ...initialPolicy,
      model_overrides: [{ origin_model_name: "Model-A", settlement_bps: 8000, sales_bps: 9500 }],
    };
    const draft = policyToDraft(base);
    draft.overrides = [];
    const policy = draftToPolicy(draft, base, false);
    expect(policy.model_overrides).toEqual([
      { origin_model_name: "Model-A", settlement_bps: 8000, sales_bps: null },
    ]);
    expect(pricingRequest(policy, false, "").model_sales_overrides).toEqual([
      { origin_model_name: "Model-A", sales_bps: null },
    ]);
  });

  test("root removing a model sends a complete replacement without that override", () => {
    const base: Policy = {
      ...initialPolicy,
      model_overrides: [{ origin_model_name: "Model-A", settlement_bps: 8000, sales_bps: 9500 }],
    };
    const draft = policyToDraft(base);
    draft.overrides = [];
    expect(pricingRequest(draftToPolicy(draft, base, true), true, "").model_overrides).toEqual([]);
  });

  test("explicit zero settlement remains zero while a blank sales value becomes null", () => {
    const draft = policyToDraft(initialPolicy);
    draft.overrides = [{ model: "Model-A", settlement: "0", sales: "" }];
    expect(draftToPolicy(draft, initialPolicy, true).model_overrides).toEqual([
      { origin_model_name: "Model-A", settlement_bps: 0, sales_bps: null },
    ]);
  });

  test("default sales change is validated against inherited model settlement rules", () => {
    const base: Policy = {
      ...initialPolicy,
      model_overrides: [{ origin_model_name: "Model-A", settlement_bps: 9000, sales_bps: 10000 }],
    };
    const draft = policyToDraft(base);
    draft.sales = "0.9000";
    draft.overrides = [];
    expect(() => draftToPolicy(draft, base, false)).toThrow(
      "Every sales coefficient must be positive",
    );
  });

  test("operator draft cannot overwrite protected settlement spread or cap fields", () => {
    const draft = policyToDraft(initialPolicy);
    draft.settlement = "0";
    draft.spread = "0";
    draft.cap = "10";
    const policy = draftToPolicy(draft, initialPolicy, false);
    expect(policy.default_settlement_bps).toBe(7500);
    expect(policy.min_spread_bps).toBe(500);
    expect(policy.sales_cap_bps).toBe(30000);
  });

  test("model names preserve case and duplicate names are rejected", () => {
    const draft = policyToDraft(initialPolicy);
    draft.overrides = [
      { model: "Model-A", settlement: "", sales: "" },
      { model: "model-a", settlement: "", sales: "" },
    ];
    expect(
      draftToPolicy(draft, initialPolicy, true).model_overrides?.map(
        (row) => row.origin_model_name,
      ),
    ).toEqual(["Model-A", "model-a"]);
    draft.overrides[1].model = "Model-A";
    expect(() => draftToPolicy(draft, initialPolicy, true)).toThrow(
      "Each model can have only one override.",
    );
  });

  test("coefficient conversion preserves precision and rejects truncation or scientific notation", () => {
    expect(parseCoefficient("0.9001")).toBe(9001);
    expect(parseCoefficient("0")).toBe(0);
    expect(() => parseCoefficient("0.90001")).toThrow();
    expect(() => parseCoefficient("1e2")).toThrow();
    expect(() => parseCoefficient("10.0001")).toThrow();
    expect(() => parseCoefficient("")).toThrow();
  });
});
