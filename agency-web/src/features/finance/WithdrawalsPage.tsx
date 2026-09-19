import { useState } from "react";
import { useTranslation } from "react-i18next";
import { PageHeader } from "../../components/PageHeader";
import { ActionIcon } from "../../components/Heading";
import {
  DataTable,
  ErrorNotice,
  Field,
  Loading,
  Pagination,
  formatMoney,
  formatTime,
} from "../../components/ui";
import { useQuery } from "../../lib/query";
import type { Identity, Page } from "../../lib/types";
import { actionLabels, statusLabels, withdrawalActions } from "./contracts";
import type { PaymentLease, Withdrawal, WithdrawalAction } from "./contracts";
import { RevealAccount } from "./AccountsPage";
import { WithdrawalActionDialog, WithdrawalForm } from "./WithdrawalForms";

export function WithdrawalsPage(props: { identity: Identity; onAddAccount?: () => void }) {
  const { t } = useTranslation();
  const isRoot = props.identity.actor_type === "root";
  const [filters, setFilters] = useState({ status: "", agency_name: "", currency_code: "" });
  const [applied, setApplied] = useState(filters);
  const [cursor, setCursor] = useState("");
  const [create, setCreate] = useState(false);
  const [selected, setSelected] = useState<{ row: Withdrawal; action: WithdrawalAction } | null>(
    null,
  );
  const [revealed, setRevealed] = useState<Withdrawal | null>(null);
  const [leases, setLeases] = useState<Record<string, PaymentLease>>({});
  const params = new URLSearchParams({ page_size: "30" });
  if (cursor) params.set("cursor", cursor);
  if (isRoot) {
    for (const [key, value] of Object.entries(applied))
      if (value.trim()) params.set(key, value.trim());
  }
  const path = `${isRoot ? "/root" : ""}/withdrawals?${params}`;
  const query = useQuery<Page<Withdrawal>>(isRoot || props.identity.agency_id ? path : null);
  if (!isRoot && !props.identity.agency_id)
    return <p className="empty">{t("Enter an agency to view its withdrawals.")}</p>;
  return (
    <section>
      <PageHeader
        icon="withdrawals"
        title={t(isRoot ? "Withdrawal review" : "Withdrawals")}
        description={t("Review commission withdrawals and track their payment status.")}
        actions={<div className="actions">
          <button className="secondary button-icon" type="button" onClick={query.reload}>
            <ActionIcon name="refresh" />
            {t("Refresh")}
          </button>
          {props.identity.agency_id && (
            <button className="button-icon" type="button" onClick={() => setCreate(true)}>
              <ActionIcon name="plus" />
              {t("Request withdrawal")}
            </button>
          )}
        </div>}
      />
      {isRoot && (
        <form
          className="filters"
          onSubmit={(event) => {
            event.preventDefault();
            setApplied(filters);
            setCursor("");
          }}
        >
          <Field label={t("Withdrawal status")}>
            <select
              value={filters.status}
              onChange={(event) => setFilters({ ...filters, status: event.target.value })}
            >
              <option value="">{t("All statuses")}</option>
              {Object.entries(statusLabels).map(([status, label]) => (
                <option key={status} value={status}>
                  {t(label)}
                </option>
              ))}
            </select>
          </Field>
          <Field label={t("Agency name")}>
            <input
              value={filters.agency_name}
              onChange={(event) => setFilters({ ...filters, agency_name: event.target.value })}
            />
          </Field>
          <Field label={t("Currency")}>
            <input
              maxLength={16}
              value={filters.currency_code}
              onChange={(event) =>
                setFilters({ ...filters, currency_code: event.target.value.toUpperCase() })
              }
            />
          </Field>
          <button className="button-icon" type="submit">
            <ActionIcon name="search" />
            {t("Apply filters")}
          </button>
        </form>
      )}
      <ErrorNotice error={query.error} />
      {query.loading ? (
        <Loading />
      ) : (
        <DataTable
          rows={query.data?.items || []}
          rowKey={(row) => String(row.id)}
          empty={t(
            isRoot
              ? "No matching withdrawals. Adjust the filters or refresh after an agency submits a request."
              : "No withdrawals yet. Add a payout account and request a withdrawal when commission is available.",
          )}
          columns={[
            { key: "request_no", label: "Withdrawal" },
            ...(isRoot ? [{ key: "agency_name", label: "Agency name" }] : []),
            {
              key: "amount_micros",
              label: "Amount",
              render: (row) => formatMoney(row.amount_micros, row.currency_code),
            },
            {
              key: "status",
              label: "Status",
              render: (row) => (
                <>
                  <span className="badge">{t(statusLabels[row.status] || row.status)}</span>
                  {row.on_hold_reason && <p className="muted">{row.on_hold_reason}</p>}
                </>
              ),
            },
            {
              key: "account_id",
              label: "Payout account",
              render: (row) => (
                <div className="inline-cell-actions">
                  <span>{row.account_label || t("Payout account")} · {t("Version")} {row.account_version}</span>
                  {isRoot && (
                    <button className="secondary button-icon compact-action" type="button" onClick={() => setRevealed(row)}>
                      <ActionIcon name="eye" />
                      {t("Reveal account")}
                    </button>
                  )}
                </div>
              ),
            },
            {
              key: "payment_reference",
              label: "Payment reference",
              render: (row) =>
                row.payment_reference ? `${row.payment_channel}: ${row.payment_reference}` : "—",
            },
            {
              key: "created_at_ms",
              label: "Created",
              render: (row) => formatTime(row.created_at_ms),
            },
            {
              key: "actions",
              label: "Actions",
              render: (row) => (
                <div className="actions">
                  {withdrawalActions(row, isRoot).map((action) => (
                    <button
                      className="secondary button-icon compact-action"
                      key={action}
                      type="button"
                      onClick={() => setSelected({ row, action })}
                    >
                      <ActionIcon name={action === "paid" || action === "approved" ? "check" : action === "rejected" || action === "cancelled" ? "close" : "play"} />
                      {t(actionLabels[action])}
                    </button>
                  ))}
                </div>
              ),
            },
          ]}
        />
      )}
      <Pagination
        nextCursor={query.data?.meta?.next_cursor}
        hasPrevious={Boolean(cursor)}
        onNext={() => setCursor(query.data?.meta?.next_cursor || "")}
        onReset={() => setCursor("")}
      />
      {create && props.identity.agency_id && (
        <WithdrawalForm
          agencyID={props.identity.agency_id}
          onClose={() => setCreate(false)}
          onAddAccount={() => {
            setCreate(false);
            props.onAddAccount?.();
          }}
          onSaved={() => {
            setCreate(false);
            setCursor("");
            query.reload();
          }}
        />
      )}
      {selected && (
        <WithdrawalActionDialog
          row={selected.row}
          action={selected.action}
          isRoot={isRoot}
          lease={leases[String(selected.row.id)]}
          onClose={() => setSelected(null)}
          onSaved={(lease) => {
            if (lease) setLeases((current) => ({ ...current, [String(selected.row.id)]: lease }));
            setSelected(null);
            query.reload();
          }}
        />
      )}
      {revealed && (
        <RevealAccount
          account={{ id: revealed.account_id, version: revealed.account_version }}
          onClose={() => setRevealed(null)}
        />
      )}
    </section>
  );
}
