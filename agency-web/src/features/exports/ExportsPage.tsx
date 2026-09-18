import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { PageHeader } from "../../components/PageHeader";
import { ActionIcon } from "../../components/Heading";
import { DataTable, ErrorNotice, Field, Loading, Pagination, Time } from "../../components/ui";
import { api, ApiError, hubConfig } from "../../lib/api";
import { useMutation } from "../../lib/mutations";
import { useQuery } from "../../lib/query";
import type { Page } from "../../lib/types";
import { defaultExportForm, exportKindLabel, exportRequest, exportStatusLabel } from "./contracts";
import type { ExportForm, ExportJob, ExportKind } from "./contracts";

export function ExportsPage({ initialKind = "usage" }: { initialKind?: ExportKind }) {
  const { t } = useTranslation();
  const [form, setForm] = useState<ExportForm>(() => defaultExportForm(new Date(), initialKind));
  const [cursor, setCursor] = useState("");
  const [error, setError] = useState<unknown>(null);
  const [downloading, setDownloading] = useState("");
  const [created, setCreated] = useState("");
  const downloadController = useRef<AbortController | null>(null);
  const mutation = useMutation();
  const query = useQuery<Page<ExportJob>>(
    `/exports?page_size=20&cursor=${encodeURIComponent(cursor)}`,
  );
  const running =
    query.data?.items.some((job) => job.status === "queued" || job.status === "processing") ||
    false;
  const reload = query.reload;
  useEffect(() => {
    if (!running) return;
    const timer = window.setInterval(reload, 3000);
    return () => window.clearInterval(timer);
  }, [running, reload]);
  useEffect(() => () => downloadController.current?.abort(), []);

  async function create(event: React.FormEvent) {
    event.preventDefault();
    setError(null);
    setCreated("");
    try {
      const job = await mutation.mutate<ExportJob>("/exports", exportRequest(form));
      setCreated(job.id || "");
      setCursor("");
      reload();
    } catch (cause) {
      setError(cause);
    }
  }

  async function download(job: ExportJob) {
    const controller = new AbortController();
    downloadController.current?.abort();
    downloadController.current = controller;
    setDownloading(job.id);
    setError(null);
    try {
      // Acquire a fresh, session-bound credential only for the clicked job.
      // Never put it in browser storage, URL history or a third-party link.
      const fresh = await api<ExportJob>(`/exports/${encodeURIComponent(job.id)}`, {
        signal: controller.signal,
      });
      if (fresh.status !== "ready" || !fresh.download_token)
        throw new Error("This export is no longer available. Create a new export.");
      const response = await fetch(
        `${hubConfig().base_path}/api/v1/exports/${encodeURIComponent(job.id)}/download`,
        {
          credentials: "include",
          cache: "no-store",
          headers: { "X-Export-Token": fresh.download_token },
          signal: controller.signal,
        },
      );
      if (!response.ok) {
        const body = await response.json().catch(() => null);
        throw new ApiError(
          body?.error?.message || "Export download failed.",
          response.status,
          body?.error?.code || "download_failed",
        );
      }
      const blob = await response.blob();
      if (controller.signal.aborted) return;
      const objectUrl = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = objectUrl;
      link.download = `agency-${job.kind}-${job.id}.csv`;
      document.body.appendChild(link);
      link.click();
      link.remove();
      URL.revokeObjectURL(objectUrl);
    } catch (cause) {
      if (!controller.signal.aborted) setError(cause);
    } finally {
      if (!controller.signal.aborted) setDownloading("");
    }
  }

  return (
    <section>
      <PageHeader icon="exports" title={t("Data exports")} description={t("Create downloadable usage, top-up and commission reports.")} actions={<button type="button" className="secondary button-icon" onClick={reload}>
          <ActionIcon name="refresh" />
          {t("Refresh")}
        </button>} />
      <p className="muted">
        {t(
          "Export dates include both days in Asia/Shanghai. Files expire after 24 hours; original records are retained.",
        )}
      </p>
      <p className="muted">
        {t(
          "Each CSV is limited to 1,000,000 rows. Use a narrower date range if the export is too large.",
        )}
      </p>
      <form className="form-grid" onSubmit={(event) => void create(event)}>
        <Field label={t("Export type")}>
          <select
            value={form.kind}
            onChange={(event) =>
              setForm({ ...form, kind: event.target.value as ExportKind, model: "" })
            }
          >
            <option value="usage">{t("Usage")}</option>
            <option value="topups">{t("Top-ups")}</option>
            <option value="commissions">{t("Commission ledger")}</option>
          </select>
        </Field>
        <Field label={t("Start date")}>
          <input
            type="date"
            required
            value={form.startDate}
            onChange={(event) => setForm({ ...form, startDate: event.target.value })}
          />
        </Field>
        <Field label={t("End date")}>
          <input
            type="date"
            required
            min={form.startDate}
            value={form.endDate}
            onChange={(event) => setForm({ ...form, endDate: event.target.value })}
          />
        </Field>
        <Field label={t("User ID (optional)")}>
          <input
            inputMode="numeric"
            value={form.userId}
            onChange={(event) => setForm({ ...form, userId: event.target.value })}
          />
        </Field>
        {form.kind !== "topups" && (
          <Field label={t("Public model (optional)")}>
            <input
              value={form.model}
              maxLength={764}
              onChange={(event) => setForm({ ...form, model: event.target.value })}
            />
          </Field>
        )}
        <Field label={t("Currency (optional)")}>
          <input
            value={form.currency}
            maxLength={16}
            onChange={(event) => setForm({ ...form, currency: event.target.value })}
          />
        </Field>
        <div className="actions">
          <button className="button-icon" type="submit" disabled={mutation.pending}>
            <ActionIcon name="download" />
            {t("Create CSV export")}
          </button>
        </div>
      </form>
      <ErrorNotice error={error || query.error} />
      {created && (
        <p className="success" role="status">
          {t("Export job created: {{id}}", { id: created })}
        </p>
      )}
      {query.loading ? (
        <Loading />
      ) : (
        <DataTable
          rows={query.data?.items || []}
          rowKey={(job) => job.id}
          columns={[
            { key: "id", label: "Export ID" },
            { key: "kind", label: "Export type", render: (job) => t(exportKindLabel(job.kind)) },
            {
              key: "status",
              label: "Status",
              render: (job) => (
                <>
                  {t(exportStatusLabel(job.status))}
                  {job.error_code === "export_row_limit" && (
                    <p className="error">
                      {t("Too many rows. Narrow the date range and create a new export.")}
                    </p>
                  )}
                  {job.error_code === "export_generation_failed" && (
                    <p className="error">
                      {t("Generation failed. Try again or contact an administrator.")}
                    </p>
                  )}
                </>
              ),
            },
            {
              key: "filter",
              label: "Date range",
              render: (job) =>
                `${job.filter.start_date || job.filter.start_at || "—"} – ${job.filter.end_date || job.filter.end_at || "—"}`,
            },
            { key: "row_count", label: "Exported rows" },
            {
              key: "created_at_ms",
              label: "Created at",
              render: (job) => <Time value={job.created_at_ms} />,
            },
          ]}
          actions={(job) => (
            <button
              type="button"
              className="secondary"
              disabled={job.status !== "ready" || Boolean(downloading)}
              onClick={() => void download(job)}
            >
              {t(downloading === job.id ? "Downloading…" : "Download CSV")}
            </button>
          )}
        />
      )}
      <Pagination
        hasPrevious={Boolean(cursor)}
        nextCursor={query.data?.meta?.next_cursor}
        onNext={() => setCursor(query.data?.meta?.next_cursor || "")}
        onReset={() => setCursor("")}
      />
    </section>
  );
}
