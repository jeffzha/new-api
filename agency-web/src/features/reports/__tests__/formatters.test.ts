import { expect, test } from "bun:test";
import { formatExpiry, formatYuan } from "../Pages";

test("report formatters show human-readable RMB values", () => {
  expect(formatYuan(958904)).toBe("¥14.00");
  expect(formatYuan(0)).toBe("¥0.00");
  expect(formatYuan(1)).toBe("¥0.000015");
  expect(formatYuan(null)).toBe("—");
});

test("report formatter treats zero expiry as never expiring", () => {
  expect(formatExpiry(0)).toBe("永不过期");
  expect(formatExpiry("")).toBe("永不过期");
});
