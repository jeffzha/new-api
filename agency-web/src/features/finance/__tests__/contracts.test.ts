import { describe, expect, test } from "bun:test";
import {
  accountMutation,
  amountToMicros,
  createWithdrawalRequest,
  paidRequest,
  transitionRequest,
  withdrawalActions,
} from "../contracts";
import type { Withdrawal } from "../contracts";

const withdrawal: Withdrawal = {
  id: 12,
  request_no: "withdrawal-12",
  currency_code: "CNY",
  amount_micros: "100000000",
  status: "submitted",
  version: 4,
  account_id: 7,
  account_version: 2,
  on_hold_reason: "",
  created_at_ms: 1789275600000,
  updated_at_ms: 1789275600000,
  payment_channel: "",
  payment_reference: "",
};

describe("withdrawal amounts", () => {
  test("currency values retain exact precision without floating point rounding", () => {
    expect(amountToMicros("100.01", "CNY")).toBe("100010000");
    expect(amountToMicros("0.001", "KWD")).toBe("1000");
    expect(amountToMicros("9999999999.99", "CNY")).toBe("9999999999990000");
    expect(amountToMicros("120", "JPY")).toBe("120000000");
  });
  test("rejects nonpositive, exponent, overflow, and unsupported precision values", () => {
    for (const amount of [
      "0",
      "-1",
      "1e3",
      "NaN",
      "Infinity",
      "1,000",
      "01",
      "1.001",
      "9223372036855",
    ]) {
      expect(() => amountToMicros(amount, "CNY")).toThrow();
    }
    expect(() => amountToMicros("0.1", "JPY")).toThrow();
  });
  test("request uses a decimal micros string and prevents spending beyond the selected currency balance", () => {
    const account = { id: 7, version: 2, last4: "1234", created_at_ms: 1 };
    const balance = {
      currency_code: "CNY",
      available_micros: "100010000",
      tax_micros: "6720672",
      withdrawable_micros: "93289328",
      locked_micros: "0",
      earned_micros: "100010000",
      reversed_micros: "0",
      net_earned_micros: "100010000",
      paid_micros: "0",
    };
    const request = createWithdrawalRequest(3, account, "93.28", balance);
    expect(request).toEqual({
      path: "/withdrawals",
      action: "withdrawal.create",
      objectId: "agency:3",
      body: { account_id: "7", currency_code: "CNY", amount_micros: "93280000" },
    });
    expect(() => createWithdrawalRequest(3, account, "93.30", balance)).toThrow();
  });
});

describe("withdrawal state and payment contracts", () => {
  test("an operator can only cancel an unreviewed request", () => {
    expect(withdrawalActions(withdrawal, false)).toEqual(["cancelled"]);
    expect(withdrawalActions({ ...withdrawal, status: "reviewing" }, false)).toEqual([]);
    expect(() =>
      transitionRequest({ ...withdrawal, status: "approved" }, "cancelled", false, "cancel"),
    ).toThrow();
    expect(transitionRequest(withdrawal, "cancelled", false, " No longer needed ")).toEqual({
      path: "/withdrawals/12/cancel",
      action: "withdrawal.cancel",
      objectId: "withdrawal:12",
      body: { expected_version: 4, reason: "No longer needed" },
    });
  });
  test("unknown or held payments require an evidence recovery action before another attempt", () => {
    expect(withdrawalActions({ ...withdrawal, status: "payment_unknown" }, true)).toEqual([
      "paid",
      "on_hold",
      "recover_unpaid",
    ]);
    expect(
      withdrawalActions({ ...withdrawal, status: "on_hold", previous_status: "paying" }, true),
    ).toEqual(["paid", "recover_unpaid"]);
    expect(withdrawalActions({ ...withdrawal, status: "paid" }, true)).toEqual([]);
  });
  test("restoring an unpaid withdrawal requires complete structured evidence and keeps the proof bound to the same version", () => {
    const row = { ...withdrawal, status: "payment_unknown" };
    expect(() =>
      transitionRequest(row, "recover_unpaid", true, "Bank rejected transfer"),
    ).toThrow();
    const evidence = {
      outcome: "not_paid" as const,
      payment_channel: "bank",
      original_payment_reference: "attempt-42",
      bank_confirmation_reference: "case-42",
      confirmed_at: "2026-09-13T10:00:00+08:00",
    };
    expect(
      transitionRequest(row, "recover_unpaid", true, "Bank rejected transfer", evidence),
    ).toEqual({
      path: "/root/withdrawals/12/transition",
      action: "withdrawal.transition",
      objectId: "withdrawal:12",
      body: {
        expected_version: 4,
        target_status: "approved",
        reason: "Bank rejected transfer",
        unpaid_evidence: evidence,
      },
    });
    expect(() => transitionRequest(row, "paying", true, "retry", evidence)).toThrow();
    expect(() =>
      transitionRequest(row, "recover_unpaid", true, "checked", {
        ...evidence,
        bank_confirmation_reference: " ",
      }),
    ).toThrow();
    expect(() => transitionRequest(row, "recover_unpaid", false, "checked", evidence)).toThrow();
  });
  test("root revocations are rejections while operator withdrawals remain cancellations", () => {
    expect(withdrawalActions({ ...withdrawal, status: "approved" }, true)).toEqual([
      "paying",
      "rejected",
      "on_hold",
    ]);
    expect(
      withdrawalActions({ ...withdrawal, status: "on_hold", previous_status: "approved" }, true),
    ).toEqual(["reviewing", "rejected"]);
  });
  test("root approval sends expected version, reason and action-bound proof scope", () => {
    expect(
      transitionRequest({ ...withdrawal, status: "reviewing" }, "approved", true, "checked"),
    ).toEqual({
      path: "/root/withdrawals/12/transition",
      action: "withdrawal.transition",
      objectId: "withdrawal:12",
      body: { expected_version: 4, reason: "checked", target_status: "approved" },
    });
    expect(() =>
      transitionRequest({ ...withdrawal, status: "reviewing" }, "approved", true, ""),
    ).toThrow();
  });
  test("marking a paying request preserves the full lease token and original bank evidence", () => {
    const lease = { payment_lease_token: "1789275600123456789", payment_lease_until: 1789276200 };
    expect(
      paidRequest({ ...withdrawal, status: "paying" }, "bank", "bank-reference", lease),
    ).toEqual({
      path: "/root/withdrawals/12/mark-paid",
      action: "withdrawal.mark_paid",
      objectId: "withdrawal:12",
      body: {
        expected_version: 4,
        payment_channel: "bank",
        payment_reference: "bank-reference",
        payment_lease_token: "1789275600123456789",
      },
    });
    expect(() => paidRequest({ ...withdrawal, status: "paying" }, "bank", "ref")).toThrow();
    expect(() => paidRequest({ ...withdrawal, status: "submitted" }, "bank", "ref")).toThrow();
  });
  test("recording a verified unknown payment does not require a stale lease or send a new-payment command", () => {
    expect(
      paidRequest({ ...withdrawal, status: "payment_unknown" }, "bank", "verified-original").body,
    ).toEqual({
      expected_version: 4,
      payment_channel: "bank",
      payment_reference: "verified-original",
    });
  });
});

test("account edits submit all replacement details and the previous version without guessing masked values", () => {
  const input = {
    account_type: "bank",
    account_name: " Example Ltd ",
    account_no: "1234567890",
    bank_name: "Example Bank",
  };
  expect(accountMutation(3, input, { id: 7, version: 2, last4: "0000", created_at_ms: 1 })).toEqual(
    {
      path: "/withdrawal-accounts/7",
      method: "PATCH",
      action: "withdrawal_account.update",
      objectId: "withdrawal_account:7",
      body: {
        account_type: "bank",
        account_name: "Example Ltd",
        account_no: "1234567890",
        bank_name: "Example Bank",
        expected_version: 2,
      },
    },
  );
  expect(() => accountMutation(3, { ...input, account_no: "" })).toThrow();
});
