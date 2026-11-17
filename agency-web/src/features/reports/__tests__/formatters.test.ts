import { expect, test } from "bun:test";
import { formatActualMoney, formatExpiry, formatFundingBreakdown, formatQuota } from "../Pages";

const usd = { currency_code: "USD", currency_symbol: "$", quota_per_unit: 500000, usd_to_currency_rate: 1 };
const cny = { currency_code: "CNY", currency_symbol: "¥", quota_per_unit: 500000, usd_to_currency_rate: 7.3 };

test("overseas quota amounts use USD without applying the domestic exchange rate", () => {
  expect(formatQuota(958904, usd)).toBe("$1.92");
  expect(formatQuota(0, usd)).toBe("$0.00");
  expect(formatQuota(1, usd)).toBe("$0.000002");
  expect(formatQuota(null, usd)).toBe("—");
  expect(formatQuota(1000000, { ...usd, quota_per_unit: 1000000 })).toBe("$1.00");
});

test("domestic quota amounts retain the configured CNY conversion", () => {
  expect(formatQuota(958904, cny)).toBe("¥14.00");
  expect(formatQuota(1, cny)).toBe("¥0.000015");
  expect(formatQuota(500000, { ...cny, usd_to_currency_rate: 7 })).toBe("¥7.00");
});

test("missing or invalid quota configuration does not invent a currency or amount", () => {
  expect(formatQuota(500000, undefined)).toBe("—");
  expect(formatQuota(500000, { ...usd, quota_per_unit: 0 })).toBe("—");
  expect(formatQuota(500000, { ...usd, usd_to_currency_rate: Number.NaN })).toBe("—");
});

test("funding allocation uses the same regional currency as charged quota", () => {
  expect(formatFundingBreakdown([{ source: "debt", quota: "500000" }], usd)).toBe("欠费: $1.00");
  expect(formatFundingBreakdown([{ source: "debt", quota: "500000" }], cny)).toBe("欠费: ¥7.30");
});

test("actual top-up payments retain their receipt currency without quota conversion", () => {
  expect(formatActualMoney("100.00", "USD")).toBe("$100.00");
  expect(formatActualMoney("100.00", "CNY")).toBe("¥100.00");
  expect(formatActualMoney("100.00", "HKD")).toBe("HKD 100.00");
  expect(formatActualMoney("100.00", "")).toBe("—");
});

test("report formatter treats zero expiry as never expiring", () => {
  expect(formatExpiry(0)).toBe("永不过期");
  expect(formatExpiry("")).toBe("永不过期");
});
