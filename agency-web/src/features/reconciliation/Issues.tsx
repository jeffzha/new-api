import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ActionIcon } from "../../components/Heading";
import {
  DataTable,
  Dialog,
  ErrorNotice,
  Field,
  Loading,
  Pagination,
  Time,
} from "../../components/ui";
import { useMutation } from "../../lib/mutations";
import { useQuery } from "../../lib/query";
import type { Page } from "../../lib/types";
import {
  resolutionRequest,
  type ReconciliationDetail,
  type ReconciliationIssue,
  type ResolutionAction,
} from "./contracts";
import { evidenceLabels } from "./labels";
import { reconciliationError } from "./errors";

export function ReconciliationIssues(props: { onChanged: () => void }) {
  const { t } = useTranslation();
  const [cursor, setCursor] = useState("");
  const [status, setStatus] = useState("");
  const [selected, setSelected] = useState("");
  const query = useQuery<Page<ReconciliationIssue>>(
    `/root/reconciliation/issues?page_size=30&status=${status}&cursor=${encodeURIComponent(cursor)}`,
  );
  return (
    <section>
      <div className="page-filter-bar reconciliation-filter-bar">
        <Field label={t("Issue status")}>
          <select
            value={status}
            onChange={(event) => {
              setStatus(event.target.value);
              setCursor("");
            }}
          >
            <option value="">{t("All statuses")}</option>
            <option value="open">{t("Open")}</option>
            <option value="resolved">{t("Resolved")}</option>
            <option value="ignored">{t("Ignored")}</option>
          </select>
        </Field>
        <button type="button" className="secondary button-icon" onClick={query.reload}>
          <ActionIcon name="refresh" />
          {t("Refresh")}
        </button>
      </div>
      <ErrorNotice error={reconciliationError(query.error)} />
      {query.loading ? (
        <Loading />
      ) : (
        <DataTable
          rows={query.data?.items || []}
          rowKey={(row) => row.id}
          columns={[
            { key: "id", label: "Issue ID" },
            { key: "object_type", label: "Object type" },
            { key: "object_id", label: "Object ID" },
            { key: "status", label: "Status", render: (row) => t(row.status) },
            { key: "difference", label: "Difference" },
          ]}
          actions={(row) => (
            <button type="button" className="secondary button-icon" onClick={() => setSelected(row.id)}>
              <ActionIcon name="eye" />
              {t("Inspect evidence")}
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
      {selected && (
        <IssueReview
          id={selected}
          onClose={() => setSelected("")}
          onChanged={() => {
            query.reload();
            props.onChanged();
          }}
        />
      )}
    </section>
  );
}

function IssueReview(props: { id: string; onClose: () => void; onChanged: () => void }) {
  const { t } = useTranslation();
  const query = useQuery<ReconciliationDetail>(`/root/reconciliation/issues/${props.id}`);
  const [busy, setBusy] = useState(false);
  return (
    <Dialog title={t("Review reconciliation issue")} onClose={props.onClose} busy={busy}>
      <button type="button" className="secondary button-icon" disabled={busy} onClick={query.reload}>
        <ActionIcon name="refresh" />
        {t("Reload evidence")}
      </button>
      <ErrorNotice error={reconciliationError(query.error)} />
      {query.loading && <Loading />}
      {query.data && (
        <IssueEvidence
          key={`${props.id}:${query.data.verification.evidence_hash}`}
          detail={query.data}
          onBusy={setBusy}
          onChanged={() => {
            query.reload();
            props.onChanged();
          }}
        />
      )}
    </Dialog>
  );
}

function IssueEvidence(props: {
  detail: ReconciliationDetail;
  onBusy: (busy: boolean) => void;
  onChanged: () => void;
}) {
  const { t } = useTranslation();
  const mutation = useMutation();
  const [note, setNote] = useState("");
  const [status, setStatus] = useState<"resolved" | "ignored">("resolved");
  const [confirmed, setConfirmed] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const { issue, verification } = props.detail;
  const mayVerify =
    verification.state === "consistent" && verification.allowed_actions.includes("verify_resolved");
  const mayRepair = verification.allowed_actions.includes("restore_delivery");
  const disabled = mutation.pending || !confirmed || !note.trim();
  async function submit(action: ResolutionAction) {
    if (disabled) return;
    setError(null);
    props.onBusy(true);
    try {
      const body = resolutionRequest(
        props.detail,
        action,
        action === "restore_delivery" ? "resolved" : status,
        note,
      );
      await mutation.mutate(`/root/reconciliation/issues/${issue.id}/resolve`, body, {
        action: "reconciliation.resolve",
        objectId: `reconciliation_issue:${issue.id}`,
        title: t(
          action === "restore_delivery"
            ? "Restore missing delivery"
            : "Confirm verified resolution",
        ),
      });
      props.onChanged();
    } catch (cause) {
      setError(cause);
    } finally {
      props.onBusy(false);
    }
  }
  return (
    <section className="reconciliation-evidence">
      <dl>
        <dt>{t("Issue ID")}</dt>
        <dd>{issue.id}</dd>
        <dt>{t("Object type")}</dt>
        <dd>{issue.object_type}</dd>
        <dt>{t("Object ID")}</dt>
        <dd>{issue.object_id}</dd>
        <dt>{t("Status")}</dt>
        <dd>{t(issue.status)}</dd>
        <dt>{t("Original discrepancy")}</dt>
        <dd>{issue.difference}</dd>
      </dl>
      {issue.status !== "open" && !issue.resolution_evidence && (
        <p className="notice">
          {t("This historical closure has no verified evidence and still requires review.")}
        </p>
      )}
      <p role="status" className={verification.state === "consistent" ? "success" : "notice"}>
        {t(
          verification.state === "consistent"
            ? "Current evidence is consistent."
            : "The issue remains open until its cause is verified and corrected.",
        )}
      </p>
      {verification.state === "unsupported" && (
        <p className="notice">
          {t(
            "The available records cannot prove a safe resolution. Investigate the original operation first.",
          )}
        </p>
      )}
      <h3>{t("Current evidence")}</h3>
      <DataTable
        rows={verification.checks}
        columns={[
          { key: "name", label: "Check", render: (row) => t(evidenceLabels[row.name] || row.name) },
          { key: "expected", label: "Expected" },
          { key: "actual", label: "Observed" },
          {
            key: "matched",
            label: "Result",
            render: (row) => t(row.matched ? "Matched" : "Mismatch"),
          },
        ]}
      />
      <details>
        <summary>{t("Evidence fingerprint")}</summary>
        <code>{verification.evidence_hash}</code>
      </details>
      {issue.resolution && (
        <section>
          <h3>{t("Recorded resolution")}</h3>
          <p>{issue.resolution}</p>
          <p>
            {t("Reviewer ID")}: {issue.actor_id || "—"} ·{" "}
            <Time value={issue.resolved_at_ms || undefined} />
          </p>
          {issue.repair_event_id && (
            <p>
              {t("Repair reference")}: {issue.repair_event_id}
            </p>
          )}
        </section>
      )}
      <ErrorNotice error={reconciliationError(error)} />
      {(mayVerify || mayRepair) && (
        <form
          onSubmit={(event) => {
            event.preventDefault();
            void submit(mayRepair ? "restore_delivery" : "verify_resolved");
          }}
        >
          {mayRepair && (
            <p className="notice">
              {t(
                "Restore only the missing delivery for the verified original event. Existing receipts prevent duplicate commission; balances and original records are preserved.",
              )}
            </p>
          )}
          {mayVerify && (
            <Field label={t("Treatment")}>
              <select
                value={status}
                disabled={mutation.pending}
                onChange={(event) => setStatus(event.target.value as "resolved" | "ignored")}
              >
                <option value="resolved">{t("Resolved after verification")}</option>
                <option value="ignored">{t("No repair needed (verified)")}</option>
              </select>
            </Field>
          )}
          <Field
            label={t("Resolution note")}
            hint={t(
              "Describe the original operation, evidence reviewed and why this treatment is appropriate.",
            )}
          >
            <textarea
              required
              maxLength={2000}
              value={note}
              disabled={mutation.pending}
              onChange={(event) => setNote(event.target.value)}
            />
          </Field>
          <label className="check">
            <input
              type="checkbox"
              checked={confirmed}
              disabled={mutation.pending}
              onChange={(event) => setConfirmed(event.target.checked)}
            />
            {t("I reviewed the evidence and this action.")}
          </label>
          <button type="submit" disabled={disabled}>
            {t(mayRepair ? "Restore missing delivery" : "Confirm verified resolution")}
          </button>
        </form>
      )}
    </section>
  );
}
