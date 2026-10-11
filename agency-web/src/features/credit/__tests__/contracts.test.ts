import { expect, test } from "bun:test";
import {
  CREDIT_QUOTA_PER_YUAN,
  emailStatusKey,
  formatCreditYuan,
  quotaToYuanInput,
  statementStatusKey,
  yuanToQuota,
} from "../contracts";

test("credit amounts round-trip between CNY input and integer quota", () => {
  const quota = yuanToQuota("1000");
  expect(quota).toBe(Math.round(1000 * CREDIT_QUOTA_PER_YUAN));
  expect(quotaToYuanInput(quota ?? undefined)).toBe("1000");
  expect(yuanToQuota("")).toBeNull();
  expect(yuanToQuota("-5")).toBeNull();
  expect(yuanToQuota("1e3")).toBeNull();
  expect(yuanToQuota("0")).toBe(0);
  expect(formatCreditYuan(958904)).toBe("¥14.00");
  expect(formatCreditYuan(0)).toBe("¥0.00");
});

test("credit statuses map onto translated statement and email labels", () => {
  expect(statementStatusKey("issued")).toBe("Issued");
  expect(statementStatusKey("overdue")).toBe("Overdue");
  expect(statementStatusKey("paid")).toBe("Settled");
  expect(statementStatusKey("bad_debt")).toBe("Bad debt");
  expect(statementStatusKey("unknown")).toBe("Status");
  expect(emailStatusKey("sent")).toBe("Sent");
  expect(emailStatusKey("failed")).toBe("Failed");
  expect(emailStatusKey("disabled")).toBe("Not sent");
  expect(emailStatusKey("pending")).toBe("Not sent");
});
