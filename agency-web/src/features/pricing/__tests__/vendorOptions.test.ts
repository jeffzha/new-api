import { describe, expect, test } from "bun:test";
import {
  activeVendorKey,
  buildVendorOptions,
  matchesPricingSearch,
  OTHER_VENDOR_KEY,
  vendorKeyOf,
} from "../vendorOptions";

describe("pricing provider filters", () => {
  test("groups models by provider and keeps unknown providers together", () => {
    const options = buildVendorOptions(
      [
        { vendor_name: "DeepSeek", vendor_icon: "DeepSeek.Color" },
        { vendor_name: "DeepSeek", vendor_icon: "DeepSeek.Color" },
        { vendor_name: "OpenAI", vendor_icon: "OpenAI" },
        {},
        { vendor_name: "  " },
      ],
      "Other providers",
    );
    expect(options).toEqual([
      { key: "DeepSeek", name: "DeepSeek", icon: "DeepSeek.Color", models: 2 },
      { key: OTHER_VENDOR_KEY, name: "Other providers", models: 2 },
      { key: "OpenAI", name: "OpenAI", icon: "OpenAI", models: 1 },
    ]);
  });

  test("keeps the selected provider only while it still exists", () => {
    const vendors = buildVendorOptions([{ vendor_name: "DeepSeek" }], "Other providers");
    expect(activeVendorKey("DeepSeek", vendors)).toBe("DeepSeek");
    expect(activeVendorKey("all", vendors)).toBe("all");
    expect(activeVendorKey("Removed provider", vendors)).toBe("all");
  });

  test("search matches the model, its provider and its channels", () => {
    const row = {
      origin_model_name: "deepseek-v4.1-flash",
      vendor_name: "DeepSeek",
      channelNames: ["uzoom-主站"],
    };
    expect(matchesPricingSearch("", row)).toBe(true);
    expect(matchesPricingSearch("V4.1", row)).toBe(true);
    expect(matchesPricingSearch("deepseek", row)).toBe(true);
    expect(matchesPricingSearch("uzoom", row)).toBe(true);
    expect(matchesPricingSearch("glm", row)).toBe(false);
  });

  test("provider keys ignore surrounding whitespace", () => {
    expect(vendorKeyOf({ vendor_name: " 腾讯 " })).toBe("腾讯");
    expect(vendorKeyOf({})).toBe(OTHER_VENDOR_KEY);
  });
});
