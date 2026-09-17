import type { MutationRequest } from "../../lib/types";

export type DecimalID = string | number;
export interface PayoutAccount {
  id: DecimalID;
  version: DecimalID;
  last4: string;
  created_at_ms: DecimalID;
}
export interface RevealedAccount extends PayoutAccount {
  agency_id: DecimalID;
  account_type: string;
  account_name: string;
  account_no: string;
  bank_name: string;
}
export interface CommissionBalance {
  currency_code: string;
  available_micros: string;
  tax_micros: string;
  withdrawable_micros: string;
  locked_micros: string;
  earned_micros: string;
  reversed_micros: string;
  net_earned_micros: string;
  paid_micros: string;
}
export interface Withdrawal {
  id: DecimalID;
  agency_id?: DecimalID;
  request_no: string;
  currency_code: string;
  amount_micros: string;
  status: string;
  version: DecimalID;
  account_id: DecimalID;
  account_version: DecimalID;
  previous_status?: string;
  on_hold_reason: string;
  created_at_ms: DecimalID;
  updated_at_ms: DecimalID;
  payment_channel: string;
  payment_reference: string;
  payment_lease_token?: string;
  payment_lease_until?: DecimalID;
}
export interface PaymentLease {
  payment_lease_token: string;
  payment_lease_until: DecimalID;
}
export interface UnpaidEvidence {
  outcome: "not_paid";
  payment_channel: string;
  original_payment_reference: string;
  bank_confirmation_reference: string;
  confirmed_at: string;
}
export interface AccountInput {
  account_type: string;
  account_name: string;
  account_no: string;
  bank_name: string;
}
export type WithdrawalAction =
  | "reviewing"
  | "approved"
  | "paying"
  | "payment_unknown"
  | "on_hold"
  | "rejected"
  | "cancelled"
  | "recover_unpaid"
  | "paid";

export const statusLabels: Record<string, string> = {
  submitted: "Submitted",
  reviewing: "Under review",
  approved: "Approved",
  paying: "Payment in progress",
  payment_unknown: "Payment result unknown",
  on_hold: "On hold",
  paid: "Paid",
  rejected: "Rejected",
  cancelled: "Cancelled",
};
export const actionLabels: Record<WithdrawalAction, string> = {
  reviewing: "Start review",
  approved: "Approve withdrawal",
  paying: "Record payment start",
  payment_unknown: "Record unknown result",
  on_hold: "Place on hold",
  rejected: "Reject withdrawal",
  cancelled: "Cancel withdrawal",
  paid: "Record confirmed payment",
  recover_unpaid: "Confirm unpaid and restore approval",
};

export function amountToMicros(value: string, currency: string): string {
  const normalized = value.trim();
  if (!/^[A-Z]{3}$/.test(currency) || !/^(0|[1-9]\d*)(\.\d+)?$/.test(normalized))
    throw new Error("Enter a valid positive amount.");
  const digits =
    new Intl.NumberFormat("en", { style: "currency", currency }).resolvedOptions()
      .maximumFractionDigits ?? 2;
  const [whole, fraction = ""] = normalized.split(".");
  if (fraction.length > Math.min(digits, 6))
    throw new Error("The amount exceeds the currency precision.");
  const micros = BigInt(whole) * 1000000n + BigInt(fraction.padEnd(6, "0"));
  if (micros <= 0n || micros > 9223372036854775807n)
    throw new Error("Enter a valid positive amount.");
  return micros.toString();
}

export function currentVersion(value: DecimalID): number {
  const version = Number(value);
  if (!Number.isSafeInteger(version) || version <= 0)
    throw new Error("Refresh this record before continuing.");
  return version;
}

export function withdrawalActions(row: Withdrawal, isRoot: boolean): WithdrawalAction[] {
  if (!isRoot) return row.status === "submitted" ? ["cancelled"] : [];
  switch (row.status) {
    case "submitted":
      return ["reviewing", "on_hold"];
    case "reviewing":
      return ["approved", "rejected", "on_hold"];
    case "approved":
      return ["paying", "rejected", "on_hold"];
    case "paying":
      return ["paid", "payment_unknown", "on_hold"];
    case "payment_unknown":
      return ["paid", "on_hold", "recover_unpaid"];
    case "on_hold":
      if (row.previous_status === "paying" || row.previous_status === "payment_unknown")
        return ["paid", "recover_unpaid"];
      return ["reviewing", "rejected"];
    default:
      return [];
  }
}

export function createWithdrawalRequest(
  agencyID: DecimalID,
  account: PayoutAccount,
  amount: string,
  balance: CommissionBalance,
): MutationRequest {
  const micros = amountToMicros(amount, balance.currency_code);
  if (BigInt(micros) > BigInt(balance.withdrawable_micros))
    throw new Error("The amount exceeds the withdrawable commission balance.");
  return {
    path: "/withdrawals",
    action: "withdrawal.create",
    objectId: `agency:${agencyID}`,
    body: {
      account_id: String(account.id),
      currency_code: balance.currency_code,
      amount_micros: micros,
    },
  };
}

export function accountMutation(
  agencyID: DecimalID,
  input: AccountInput,
  previous?: PayoutAccount,
): MutationRequest {
  const body = {
    account_type: input.account_type.trim(),
    account_name: input.account_name.trim(),
    account_no: input.account_no.trim(),
    bank_name: input.bank_name.trim(),
  };
  if (
    !body.account_type ||
    !body.account_name ||
    !body.bank_name ||
    body.account_no.length < 4 ||
    body.account_no.length > 128
  )
    throw new Error("Complete all payout account fields.");
  if (previous)
    return {
      path: `/withdrawal-accounts/${previous.id}`,
      method: "PATCH",
      action: "withdrawal_account.update",
      objectId: `withdrawal_account:${previous.id}`,
      body: { ...body, expected_version: currentVersion(previous.version) },
    };
  return {
    path: "/withdrawal-accounts",
    action: "withdrawal_account.create",
    objectId: `agency:${agencyID}`,
    body,
  };
}

export function transitionRequest(
  row: Withdrawal,
  action: WithdrawalAction,
  isRoot: boolean,
  reason: string,
  unpaidEvidence?: UnpaidEvidence,
): MutationRequest {
  if (!withdrawalActions(row, isRoot).includes(action) || action === "paid")
    throw new Error("This action is unavailable for the current status.");
  if (!reason.trim()) throw new Error("Enter a reason for this action.");
  const body = { expected_version: currentVersion(row.version), reason: reason.trim() };
  if (action === "recover_unpaid") {
    if (
      !unpaidEvidence ||
      unpaidEvidence.outcome !== "not_paid" ||
      !unpaidEvidence.payment_channel.trim() ||
      unpaidEvidence.payment_channel.trim().length > 64 ||
      !unpaidEvidence.original_payment_reference.trim() ||
      unpaidEvidence.original_payment_reference.trim().length > 191 ||
      !unpaidEvidence.bank_confirmation_reference.trim() ||
      unpaidEvidence.bank_confirmation_reference.trim().length > 191 ||
      !Number.isFinite(Date.parse(unpaidEvidence.confirmed_at))
    )
      throw new Error(
        "Enter the bank investigation details confirming that the original payment was not made.",
      );
    return {
      path: `/root/withdrawals/${row.id}/transition`,
      action: "withdrawal.transition",
      objectId: `withdrawal:${row.id}`,
      body: { ...body, target_status: "approved", unpaid_evidence: unpaidEvidence },
    };
  }
  if (!isRoot)
    return {
      path: `/withdrawals/${row.id}/cancel`,
      action: "withdrawal.cancel",
      objectId: `withdrawal:${row.id}`,
      body,
    };
  return {
    path: `/root/withdrawals/${row.id}/transition`,
    action: "withdrawal.transition",
    objectId: `withdrawal:${row.id}`,
    body: { ...body, target_status: action },
  };
}

export function paidRequest(
  row: Withdrawal,
  channel: string,
  reference: string,
  lease?: PaymentLease,
): MutationRequest {
  if (!withdrawalActions(row, true).includes("paid"))
    throw new Error("This action is unavailable for the current status.");
  if (
    !channel.trim() ||
    !reference.trim() ||
    channel.trim().length > 64 ||
    reference.trim().length > 191
  )
    throw new Error("Enter the payment channel and original bank reference.");
  const body: Record<string, string | number> = {
    expected_version: currentVersion(row.version),
    payment_channel: channel.trim(),
    payment_reference: reference.trim(),
  };
  if (row.status === "paying") {
    const token = lease?.payment_lease_token || row.payment_lease_token;
    if (!token || typeof token !== "string" || !/^[1-9]\d*$/.test(token))
      throw new Error(
        "The payment lease is unavailable. Verify the original payment result first.",
      );
    body.payment_lease_token = token;
  }
  return {
    path: `/root/withdrawals/${row.id}/mark-paid`,
    action: "withdrawal.mark_paid",
    objectId: `withdrawal:${row.id}`,
    body,
  };
}
