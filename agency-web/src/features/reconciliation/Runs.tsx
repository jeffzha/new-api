import { useState } from "react";
import { useTranslation } from "react-i18next";
import { DataTable, ErrorNotice, Loading, Pagination, Time } from "../../components/ui";
import { useQuery } from "../../lib/query";
import type { Page } from "../../lib/types";

interface ReconciliationRun {
  id: string;
  status: string;
  trigger: string;
  started_at_ms: string;
  finished_at_ms?: string;
  error: string;
}

export function ReconciliationRuns() {
  const { t } = useTranslation();
  const [cursor, setCursor] = useState("");
  const query = useQuery<Page<ReconciliationRun>>(
    `/root/reconciliation/runs?page_size=10&cursor=${encodeURIComponent(cursor)}`,
  );
  return (
    <section>
      <div className="toolbar">
        <h3>{t("Reconciliation history")}</h3>
        <button type="button" className="secondary" onClick={query.reload}>
          {t("Refresh")}
        </button>
      </div>
      <ErrorNotice error={query.error} />
      {query.loading ? (
        <Loading />
      ) : (
        <DataTable
          rows={query.data?.items || []}
          rowKey={(row) => row.id}
          columns={[
            { key: "id", label: "Run ID" },
            { key: "status", label: "Status", render: (row) => t(row.status) },
            {
              key: "started_at_ms",
              label: "Started at",
              render: (row) => <Time value={row.started_at_ms} />,
            },
            {
              key: "finished_at_ms",
              label: "Finished at",
              render: (row) => <Time value={row.finished_at_ms} />,
            },
            { key: "error", label: "Error details" },
          ]}
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
