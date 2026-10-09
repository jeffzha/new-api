import { describe, expect, test } from "bun:test";
import {
  adjustCoefficientValues,
  adjustCustomerSalesValues,
  draftToPolicy,
  initialPolicy,
  parseCoefficient,
  parseSignedCoefficient,
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

  test("signed coefficient conversion preserves exact basis points", () => {
    expect(parseSignedCoefficient("+0.1001")).toBe(1001);
    expect(parseSignedCoefficient("-0.1001")).toBe(-1001);
    expect(parseSignedCoefficient("0.0001")).toBe(1);
    expect(() => parseSignedCoefficient("-0.10001")).toThrow();
  });

  test("batch customer adjustment keeps valid changes and reports invalid rows", () => {
    const values = { valid: "0.9000", belowCost: "0.8000", aboveCap: "1.1900", untouched: "0.9500" };
    const result = adjustCustomerSalesValues(
      [
        { model: "valid", agencyCostBPS: 7000, inheritedSalesBPS: 9000 },
        { model: "belowCost", agencyCostBPS: 8000, inheritedSalesBPS: 9000 },
        { model: "aboveCap", agencyCostBPS: 7000, inheritedSalesBPS: 9000 },
        { model: "untouched", agencyCostBPS: 7000, inheritedSalesBPS: 9000 },
      ],
      values,
      ["valid", "belowCost", "aboveCap"],
      200,
      500,
      12000,
    );
    expect(result.values).toEqual({ ...values, valid: "0.9200" });
    expect(result.errors).toEqual({ belowCost: "below_spread", aboveCap: "above_cap" });
    expect(result.updated).toBe(1);
  });

  test("batch customer adjustment permits the exact agency cost", () => {
    const result = adjustCustomerSalesValues(
      [{ model: "model-a", agencyCostBPS: 8000, inheritedSalesBPS: 9000 }],
      { "model-a": "0.8500" },
      ["model-a"],
      -500,
      500,
      30000,
    );
    expect(result).toEqual({ values: { "model-a": "0.8000" }, errors: {}, updated: 1 });
  });

  test("bulk coefficient adjustment from cost rewrites selected rows and reports missing costs", () => {
    const rows = [
      { model: "with-cost", costBPS: 8000, inheritedBPS: 9000 },
      { model: "no-cost", costBPS: null, inheritedBPS: 9000 },
      { model: "untouched", costBPS: 7000, inheritedBPS: 8000 },
    ];
    const result = adjustCoefficientValues(
      rows,
      { untouched: "0.8000", "with-cost": "0.8500" },
      ["with-cost", "no-cost"],
      1000,
      { anchor: "cost" },
    );
    expect(result.values).toEqual({ untouched: "0.8000", "with-cost": "0.9000" });
    expect(result.errors).toEqual({ "no-cost": "missing_cost" });
    expect(result.updated).toBe(1);
  });

  test("bulk coefficient adjustment from the drafted value enforces cost, spread and cap", () => {
    const rows = [
      { model: "inherited", costBPS: 8000, inheritedBPS: 9000 },
      { model: "invalid", costBPS: 7000, inheritedBPS: 9000 },
      { model: "below-cost", costBPS: 8000, inheritedBPS: 9000 },
      { model: "below-spread", costBPS: 8000, inheritedBPS: 9000 },
      { model: "above-cap", costBPS: 8000, inheritedBPS: 9000 },
    ];
    const result = adjustCoefficientValues(
      rows,
      {
        invalid: "not-a-number",
        "below-cost": "0.7500",
        "below-spread": "0.8200",
        "above-cap": "9.9900",
      },
      rows.map((row) => row.model),
      200,
      { anchor: "current", minSpreadBPS: 500, capBPS: 100000 },
    );
    expect(result.values).toEqual({
      invalid: "not-a-number",
      "below-cost": "0.7500",
      "below-spread": "0.8200",
      "above-cap": "9.9900",
      inherited: "0.9200",
    });
    expect(result.errors).toEqual({
      invalid: "invalid_current",
      "below-cost": "below_cost",
      "below-spread": "below_spread",
      "above-cap": "above_cap",
    });
    expect(result.updated).toBe(1);
  });

  test("bulk coefficient adjustment allows the exact cost and rejects an unknown anchor", () => {
    const exact = adjustCoefficientValues(
      [{ model: "model-a", costBPS: 8000, inheritedBPS: 9000 }],
      { "model-a": "0.7500" },
      ["model-a"],
      500,
      { anchor: "current", minSpreadBPS: 500 },
    );
    expect(exact.errors).toEqual({});
    expect(exact.values).toEqual({ "model-a": "0.8000" });

    const unknown = adjustCoefficientValues(
      [{ model: "model-b", costBPS: null, inheritedBPS: null }],
      {},
      ["model-b"],
      500,
      { anchor: "current" },
    );
    expect(unknown.errors).toEqual({ "model-b": "invalid_current" });
    expect(unknown.updated).toBe(0);
  });
});
