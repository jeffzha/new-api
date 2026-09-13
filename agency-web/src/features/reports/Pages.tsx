import { useState } from "react";
import { useTranslation } from "react-i18next";
import { DataTable, ErrorNotice, Loading, Money, Pagination, Time } from "../../components/ui";
import { useQuery } from "../../lib/query";
import { useMutation } from "../../lib/mutations";
import type { Column, Identity, Page } from "../../lib/types";
import type { CommissionBalance } from "../finance/contracts";
import { CustomerManagementDialog } from "../customers/Management";
import { ReconciliationIssues } from "../reconciliation/Issues";
import { ReconciliationRuns } from "../reconciliation/Runs";

type Row = Record<string, string | number | null>;
interface Customer {
  user_id: string | number;
  username: string;
  agency_id?: string;
  effective_at_ms: string;
  revision: string;
}
interface SyncStatus {
  schema: { ready: boolean };
  capabilities: Record<string, boolean>;
  backlog: {
    deliveries: Record<string, number>;
    open_reconciliation_issues: number;
    exports_in_progress: number;
  };
}

export function OverviewPage({ identity }: { identity: Identity }) {
  const { t } = useTranslation();
  const balances = useQuery<{ items: CommissionBalance[] }>(
    identity.agency_id ? "/commissions/summary" : null,
  );
  if (!identity.agency_id) return <SyncStatusView />;
  return (
    <section>
      <div className="toolbar">
        <h2>{t("Overview")}</h2>
        <button type="button" className="secondary" onClick={balances.reload}>
          {t("Refresh")}
        </button>
      </div>
      <ErrorNotice error={balances.error} />
      {balances.loading && <Loading />}
      {!balances.loading && !balances.error && !balances.data?.items.length && (
        <p className="empty">{t("No commission has been recorded yet.")}</p>
      )}
      {balances.data?.items.map((balance) => (
        <section key={balance.currency_code}>
          <h3>{balance.currency_code}</h3>
          <div className="metrics commission-metrics">
            {(
              [
                { key: "available_micros", label: "Available commission" },
                { key: "paid_micros", label: "Withdrawn commission" },
                { key: "net_earned_micros", label: "Net total commission" },
                { key: "earned_micros", label: "Total earned commission" },
                { key: "reversed_micros", label: "Reversed commission" },
                { key: "locked_micros", label: "Locked commission" },
              ] as const
            ).map(({ key, label }) => (
              <article key={key}>
                <span>{t(label)}</span>
                <strong>
                  <Money value={balance[key]} currency={balance.currency_code} />
                </strong>
              </article>
            ))}
          </div>
        </section>
      ))}
      {Boolean(balances.data?.items.length) && (
        <p className="muted">
          {t(
            "Net total commission equals total earned minus reversed commission. Withdrawn and locked amounts are included in this total. Each currency is calculated separately.",
          )}
        </p>
      )}
    </section>
  );
}

export function CustomersPage({
  identity,
  onEnter,
}: {
  identity: Identity;
  onEnter: (id: string) => Promise<void>;
}) {
  const { t } = useTranslation();
  const [cursor, setCursor] = useState("");
  const [selected, setSelected] = useState<Customer | null>(null);
  const [management, setManagement] = useState<{ userID?: string } | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const global = identity.actor_type === "root" && !identity.agency_id;
  const query = useQuery<Page<Customer>>(
    `${global ? "/root" : ""}/customers?page_size=30&cursor=${encodeURIComponent(cursor)}`,
  );
  return (
    <section>
      <div className="toolbar">
        <h2>{t("Customers")}</h2>
        {identity.actor_type === "root" && (
          <button type="button" onClick={() => setManagement({})}>
            {t("Customer assignment")}
          </button>
        )}
        <button type="button" className="secondary" onClick={query.reload}>
          {t("Refresh")}
        </button>
      </div>
      <ErrorNotice error={query.error || error} />
      {management && (
        <CustomerManagementDialog
          userID={management.userID}
          onClose={() => setManagement(null)}
          onChanged={query.reload}
        />
      )}
      {selected ? (
        <>
          <button className="secondary" type="button" onClick={() => setSelected(null)}>
            {t("Back to customers")}
          </button>
          <CustomerHistory key={String(selected.user_id)} customer={selected} />
        </>
      ) : (
        <>
          {query.loading ? (
            <Loading />
          ) : (
            <DataTable
              rows={query.data?.items || []}
              rowKey={(row) => String(row.user_id)}
              columns={[
                { key: "user_id", label: "User ID" },
                { key: "username", label: "Username" },
                ...(global ? [{ key: "agency_id", label: "Agency ID" }] : []),
                {
                  key: "effective_at_ms",
                  label: "Bound at",
                  render: (row) => <Time value={row.effective_at_ms} />,
                },
              ]}
              actions={(row) => (
                <>
                  {identity.actor_type === "root" && (
                    <button
                      type="button"
                      className="secondary"
                      onClick={() => setManagement({ userID: String(row.user_id) })}
                    >
                      {t("Manage assignment")}
                    </button>
                  )}
                  <button
                    type="button"
                    className="secondary"
                    disabled={busy}
                    onClick={() => {
                      if (!global) {
                        setSelected(row);
                        return;
                      }
                      setBusy(true);
                      setError(null);
                      void onEnter(String(row.agency_id))
                        .catch(setError)
                        .finally(() => setBusy(false));
                    }}
                  >
                    {t(global ? "Enter agency" : "View usage and top-ups")}
                  </button>
                </>
              )}
            />
          )}
          <Pagination
            hasPrevious={Boolean(cursor)}
            nextCursor={query.data?.meta?.next_cursor}
            onNext={() => setCursor(query.data?.meta?.next_cursor || "")}
            onReset={() => setCursor("")}
          />
        </>
      )}
    </section>
  );
}

function CustomerHistory({ customer }: { customer: Customer }) {
  const { t } = useTranslation();
  const [tab, setTab] = useState<"usage" | "topups">("usage");
  const usageColumns: Column<Row>[] = [
    {
      key: "occurred_at_ms",
      label: "Time",
      render: (row) => <Time value={row.occurred_at_ms || undefined} />,
    },
    { key: "model", label: "Public model" },
    { key: "endpoint", label: "Endpoint" },
    { key: "business_status", label: "Status" },
    { key: "standard_quota", label: "Standard quota" },
    { key: "sales_bps", label: "Sales coefficient (bps)" },
    { key: "charged_quota", label: "Charged quota" },
    { key: "skip_reason", label: "Commission exclusion reason" },
    { key: "event_id", label: "Event ID" },
  ];
  const topupColumns: Column<Row>[] = [
    {
      key: "occurred_at_ms",
      label: "Time",
      render: (row) => <Time value={row.occurred_at_ms || undefined} />,
    },
    { key: "actual_money", label: "Actual payment" },
    { key: "currency_code", label: "Currency" },
    { key: "credited_quota", label: "Credited quota" },
    { key: "paid_quota", label: "Paid quota" },
    { key: "bonus_quota", label: "Bonus quota" },
    { key: "refunded_quota", label: "Refunded quota" },
    { key: "payment_status", label: "Payment status" },
    { key: "source_operation_id", label: "Operation ID" },
  ];
  return (
    <section>
      <h3>
        {customer.username} · {customer.user_id}
      </h3>
      <div className="tabs">
        <button
          type="button"
          className={tab === "usage" ? "tab active" : "tab"}
          onClick={() => setTab("usage")}
        >
          {t("Usage")}
        </button>
        <button
          type="button"
          className={tab === "topups" ? "tab active" : "tab"}
          onClick={() => setTab("topups")}
        >
          {t("Top-ups")}
        </button>
      </div>
      <PagedReport
        key={tab}
        path={`/customers/${customer.user_id}/${tab}`}
        columns={tab === "usage" ? usageColumns : topupColumns}
      />
    </section>
  );
}

function PagedReport({ path, columns }: { path: string; columns: Column<Row>[] }) {
  const { t } = useTranslation();
  const [cursor, setCursor] = useState("");
  const query = useQuery<Page<Row>>(`${path}?page_size=30&cursor=${encodeURIComponent(cursor)}`);
  return (
    <>
      <div className="toolbar">
        <button type="button" className="secondary" onClick={query.reload}>
          {t("Refresh")}
        </button>
      </div>
      <ErrorNotice error={query.error} />
      {query.loading ? <Loading /> : <DataTable rows={query.data?.items || []} columns={columns} />}
      <Pagination
        hasPrevious={Boolean(cursor)}
        nextCursor={query.data?.meta?.next_cursor}
        onNext={() => setCursor(query.data?.meta?.next_cursor || "")}
        onReset={() => setCursor("")}
      />
    </>
  );
}

export function LedgerPage() {
  const { t } = useTranslation();
  return (
    <section>
      <h2>{t("Commission ledger")}</h2>
      <PagedReport
        path="/commissions/ledger"
        columns={[
          {
            key: "occurred_at_ms",
            label: "Time",
            render: (row) => <Time value={row.occurred_at_ms || undefined} />,
          },
          { key: "user_id", label: "User ID" },
          { key: "origin_model_name", label: "Public model" },
          { key: "entry_type", label: "Entry type" },
          {
            key: "amount_micros",
            label: "Commission",
            render: (row) => (
              <Money value={row.amount_micros || "0"} currency={String(row.currency_code)} />
            ),
          },
          { key: "commission_quota", label: "Commission quota" },
          { key: "paid_allocated_quota", label: "Paid funding quota" },
          { key: "event_id", label: "Event ID" },
        ]}
      />
    </section>
  );
}

export function AuditPage({ root }: { root: boolean }) {
  const { t } = useTranslation();
  return (
    <section>
      <h2>{t("Audit log")}</h2>
      <PagedReport
        path={root ? "/root/audit" : "/audit"}
        columns={[
          {
            key: root ? "CreatedAtMS" : "created_at_ms",
            label: "Time",
            render: (row) => (
              <Time value={(root ? row.CreatedAtMS : row.created_at_ms) || undefined} />
            ),
          },
          { key: root ? "Action" : "action", label: "Action" },
          { key: root ? "ObjectType" : "object_type", label: "Object type" },
          { key: root ? "ObjectID" : "object_id", label: "Object ID" },
          { key: root ? "Reason" : "reason", label: "Reason" },
          { key: root ? "RequestID" : "request_id", label: "Request ID" },
        ]}
      />
    </section>
  );
}

function SyncStatusView() {
  const { t } = useTranslation();
  const query = useQuery<SyncStatus>("/root/sync/status");
  return (
    <section>
      <div className="toolbar">
        <h2>{t("Service status")}</h2>
        <button type="button" className="secondary" onClick={query.reload}>
          {t("Refresh")}
        </button>
      </div>
      <ErrorNotice error={query.error} />
      {query.loading && <Loading />}
      {query.data && (
        <>
          <p className={query.data.schema.ready ? "success" : "notice"}>
            {t(
              query.data.schema.ready
                ? "Database schema is ready."
                : "Database schema is incomplete.",
            )}
          </p>
          <DataTable
            rows={Object.entries(query.data.capabilities).map(([name, value]) => ({ name, value }))}
            columns={[
              { key: "name", label: "Capability" },
              {
                key: "value",
                label: "Status",
                render: (row) => t(row.value ? "Enabled" : "Disabled"),
              },
            ]}
          />
          <h3>{t("Pending work")}</h3>
          <DataTable
            rows={Object.entries({
              ...query.data.backlog.deliveries,
              open_reconciliation_issues: query.data.backlog.open_reconciliation_issues,
              exports_in_progress: query.data.backlog.exports_in_progress,
            }).map(([name, value]) => ({ name, value }))}
            columns={[
              { key: "name", label: "Queue" },
              { key: "value", label: "Count" },
            ]}
          />
        </>
      )}
    </section>
  );
}

export function SyncPage() {
  const { t } = useTranslation();
  const mutation = useMutation();
  const [error, setError] = useState<unknown>(null);
  const [revision, setRevision] = useState(0);
  const [statusRevision, setStatusRevision] = useState(0);
  async function run() {
    setError(null);
    try {
      await mutation.mutate(
        "/root/reconciliation/runs",
        {},
        {
          action: "reconciliation.run",
          objectId: "reconciliation:run",
          title: t("Run reconciliation"),
        },
      );
      setRevision((value) => value + 1);
    } catch (cause) {
      setError(cause);
    }
  }
  return (
    <section>
      <SyncStatusView key={`${revision}:${statusRevision}`} />
      <div className="toolbar">
        <h2>{t("Reconciliation")}</h2>
        <button type="button" disabled={mutation.pending} onClick={() => void run()}>
          {t("Run reconciliation")}
        </button>
      </div>
      <p className="muted">
        {t(
          "A reconciliation run checks current records. Its recorded cutoff is not a historical snapshot.",
        )}
      </p>
      <ErrorNotice error={error} />
      <ReconciliationIssues
        key={revision}
        onChanged={() => setStatusRevision((value) => value + 1)}
      />
      <ReconciliationRuns key={`runs:${revision}`} />
    </section>
  );
}
