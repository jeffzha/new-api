import { describe, expect, test } from "bun:test";
import { childPricingOverridePayload } from "../childPricing";

describe("child pricing editor", () => {
  test("keeps one model-level cost override for models with multiple channels", () => {
    const rows = [
      {
        origin_model_name: "deepseek-v4-flash",
        settlement_bps: 7600,
        channels: [
          { channel_id: 11, channel_name: "primary" },
          { channel_id: 19, channel_name: "backup" },
        ],
      },
    ];
    expect(childPricingOverridePayload(rows, { "deepseek-v4-flash": "0.8200" })).toEqual([
      { origin_model_name: "deepseek-v4-flash", settlement_bps: 8200 },
    ]);
  });

  test("retains an existing value when the editor input is untouched", () => {
    expect(
      childPricingOverridePayload(
        [{ origin_model_name: "glm-5.3", settlement_bps: 7500 }],
        {},
      ),
    ).toEqual([{ origin_model_name: "glm-5.3", settlement_bps: 7500 }]);
  });
});
