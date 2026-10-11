// Credit is a platform-wide contract granted by the super administrator. The
// limit and the outstanding usage stay integer quota units on the wire; the
// Agency Center renders and edits them in CNY, using the same stable
// approximation as the rest of the customer reports (500,000 quota per USD,
// 7.3 CNY per USD).
export const CREDIT_QUOTA_PER_YUAN = 500_000 / 7.3;

export interface CreditAccount {
  user_id: string;
  username: string;
  account_name?: string;
  configured: boolean;
  status: string;
  credit_limit: number;
  used_quota: number;
  available_quota: number;
  payment_term_days?: number;
  repayment_day?: number;
  email_enabled?: boolean;
  next_statement_at_ms?: number;
  updated_at_ms?: number;
}

export interface CreditStatement {
  id: number;
  user_id: number;
  username: string;
  account_name?: string;
  period: string;
  amount_quota: number;
  paid_quota: number;
  bad_debt_quota: number;
  remaining_quota: number;
  status: string;
  due_at_ms: number;
  email_status: string;
  payment_reference?: string;
}

export function quotaToYuanInput(quota: number | undefined): string {
  const value = Number(quota ?? 0);
  if (!Number.isFinite(value) || value <= 0) return "";
  const yuan = Math.round((value / CREDIT_QUOTA_PER_YUAN) * 100) / 100;
  return String(yuan);
}

export function yuanToQuota(input: string): number | null {
  const trimmed = input.trim();
  if (trimmed === "") return null;
  if (!/^\d+(\.\d{1,6})?$/.test(trimmed)) return null;
  const quota = Math.round(Number(trimmed) * CREDIT_QUOTA_PER_YUAN);
  return Number.isSafeInteger(quota) && quota >= 0 ? quota : null;
}

export function formatCreditYuan(quota: number | undefined): string {
  const value = Number(quota ?? 0);
  if (!Number.isFinite(value)) return "—";
  const yuan = value / CREDIT_QUOTA_PER_YUAN;
  const digits = Math.abs(yuan) > 0 && Math.abs(yuan) < 0.01 ? 6 : 2;
  return `¥${yuan.toLocaleString("zh-CN", {
    minimumFractionDigits: digits,
    maximumFractionDigits: digits,
  })}`;
}

// Statement and email statuses are stored as stable machine values; the UI
// maps them onto translated labels instead of showing the raw code.
export function statementStatusKey(status: string): string {
  return (
    {
      issued: "Issued",
      overdue: "Overdue",
      paid: "Settled",
      bad_debt: "Bad debt",
    }[status] ?? "Status"
  );
}

export function emailStatusKey(status: string): string {
  if (status === "sent") return "Sent";
  if (status === "failed") return "Failed";
  return "Not sent";
}
