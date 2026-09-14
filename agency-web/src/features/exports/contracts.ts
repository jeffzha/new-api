export type ExportKind = "usage" | "topups" | "commissions";
export interface ExportJob {
  id: string;
  kind: ExportKind;
  status: "queued" | "processing" | "ready" | "failed" | "expired";
  row_count: string;
  created_at_ms: string;
  expires_at: string;
  file_hash: string;
  error_code?: string;
  filter: Record<string, string>;
  download_token?: string;
}
export interface ExportForm {
  kind: ExportKind;
  startDate: string;
  endDate: string;
  userId: string;
  model: string;
  currency: string;
}

export function defaultExportForm(now = new Date(), kind: ExportKind = "usage"): ExportForm {
  const localDay = new Date(now.getTime() + 8 * 60 * 60 * 1000);
  const endDate = localDay.toISOString().slice(0, 10);
  localDay.setUTCDate(localDay.getUTCDate() - 6);
  return {
    kind,
    startDate: localDay.toISOString().slice(0, 10),
    endDate,
    userId: "",
    model: "",
    currency: "",
  };
}

export function exportRequest(form: ExportForm) {
  for (const value of [form.startDate, form.endDate]) {
    const parsed = new Date(value + "T00:00:00Z");
    if (
      !/^\d{4}-\d{2}-\d{2}$/.test(value) ||
      Number.isNaN(parsed.getTime()) ||
      parsed.toISOString().slice(0, 10) !== value
    )
      throw new Error("Choose valid start and end dates.");
  }
  if (form.startDate > form.endDate)
    throw new Error("The end date must be on or after the start date.");
  const filter: Record<string, string> = { start_date: form.startDate, end_date: form.endDate };
  if (form.userId.trim()) {
    const userId = form.userId.trim();
    if (!/^[1-9]\d*$/.test(userId) || BigInt(userId) > 9223372036854775807n)
      throw new Error("Enter a positive user ID within the supported range.");
    filter.user_id = userId;
  }
  if (form.kind !== "topups" && form.model.trim()) filter.model = form.model.trim();
  if (form.currency.trim()) filter.currency = form.currency.trim().toUpperCase();
  return { kind: form.kind, filter };
}

export function exportStatusLabel(status: ExportJob["status"]): string {
  const labels = {
    queued: "Queued",
    processing: "Generating CSV",
    ready: "Ready to download",
    failed: "Export failed",
    expired: "Expired",
  };
  return labels[status] || status;
}

export function exportKindLabel(kind: ExportKind): string {
  return { usage: "Usage", topups: "Top-ups", commissions: "Commission ledger" }[kind];
}
