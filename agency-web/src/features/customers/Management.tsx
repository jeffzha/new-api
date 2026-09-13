import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { DataTable, Dialog, ErrorNotice, Field, Loading } from "../../components/ui";
import { useMutation } from "../../lib/mutations";
import { useQuery } from "../../lib/query";
import type { Agency } from "../agencies/types";

interface ProvisioningJob {
  id: string;
  user_id: string;
  status: string;
  block_reason: string;
  cancel_reason: string;
  blocking_tasks: { id: string | number; task_id: string; status: string; progress: string }[];
}
interface CustomerManagement {
  user_id: string;
  username: string;
  billing_mode: string;
  agency_id?: string;
  binding_revision?: string;
  provisioning?: ProvisioningJob;
}

export function CustomerManagementDialog(props: {
  userID?: string;
  onClose: () => void;
  onChanged: () => void;
}) {
  const { t } = useTranslation();
  const [input, setInput] = useState(props.userID || "");
  const [userID, setUserID] = useState(props.userID || "");
  const query = useQuery<CustomerManagement>(userID ? `/root/users/${userID}/management` : null);
  return (
    <Dialog title={t("Customer assignment")} onClose={props.onClose}>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          setUserID(input);
        }}
      >
        <Field label={t("User ID")}>
          <input
            inputMode="numeric"
            pattern="[1-9][0-9]{0,18}"
            required
            value={input}
            onChange={(event) => setInput(event.target.value)}
          />
        </Field>
        <button type="submit">{t("Look up customer")}</button>
      </form>
      <ErrorNotice error={query.error} />
      {query.loading && <Loading />}
      {query.data && (
        <AssignmentForm
          key={`${userID}:${query.data.binding_revision || "legacy"}`}
          customer={query.data}
          changed={() => {
            query.reload();
            props.onChanged();
          }}
        />
      )}
    </Dialog>
  );
}

function AssignmentForm(props: { customer: CustomerManagement; changed: () => void }) {
  const { t } = useTranslation();
  const mutation = useMutation();
  const [targetInput, setTargetInput] = useState("");
  const [targetID, setTargetID] = useState("");
  const [reason, setReason] = useState("");
  const [confirmed, setConfirmed] = useState(false);
  const [jobID, setJobID] = useState(props.customer.provisioning?.id || "");
  const [error, setError] = useState<unknown>(null);
  const target = useQuery<Agency>(targetID ? `/root/agencies/${targetID}` : null);
  const customer = props.customer;
  const existingJob = customer.provisioning;
  const initialBusy =
    existingJob && ["queued", "processing", "blocked"].includes(existingJob.status);
  const transfer = Boolean(customer.agency_id);
  async function submit() {
    if (!target.data || target.data.status !== "active" || !confirmed) return;
    setError(null);
    try {
      if (transfer) {
        await mutation.mutate(
          `/root/users/${customer.user_id}/transfer`,
          {
            target_agency_id: String(target.data.id),
            expected_binding_revision: customer.binding_revision,
            reason,
          },
          {
            action: "user.transfer",
            objectId: `user:${customer.user_id}`,
            title: t("Transfer customer"),
          },
        );
      } else {
        const result = await mutation.mutate<{ job_id: string }>(
          `/root/users/${customer.user_id}/bind`,
          {
            invite_code: target.data.invite_code,
            reason,
          },
          {
            action: "user.bind",
            objectId: `user:${customer.user_id}`,
            title: t("Bind existing customer"),
          },
        );
        setJobID(result.job_id);
      }
      props.changed();
    } catch (cause) {
      setError(cause);
    }
  }
  return (
    <section>
      <h3>
        {customer.username} · {customer.user_id}
      </h3>
      <p>
        {t("Current agency")}: {customer.agency_id || t("Unassigned")}
      </p>
      {jobID && <ProvisioningProgress id={jobID} onChanged={props.changed} />}
      {!initialBusy && !transfer && (
        <p className="notice">
          {t(
            "Binding pauses new funding requests for this customer while existing tasks finish. The opening balance is nonpaid; other customers are unaffected.",
          )}
        </p>
      )}
      {transfer && (
        <p className="notice">
          {t(
            "Transfer applies to new transactions. Existing charges keep their original agency, and the customer balance is preserved.",
          )}
        </p>
      )}
      {!initialBusy && (
        <>
          <form
            onSubmit={(event) => {
              event.preventDefault();
              setTargetID(targetInput);
              setConfirmed(false);
            }}
          >
            <Field label={t("Target agency ID")}>
              <input
                inputMode="numeric"
                pattern="[1-9][0-9]{0,18}"
                required
                value={targetInput}
                disabled={mutation.pending}
                onChange={(event) => {
                  setTargetInput(event.target.value);
                  setTargetID("");
                  setConfirmed(false);
                }}
              />
            </Field>
            <button className="secondary" disabled={mutation.pending}>
              {t("Check target agency")}
            </button>
          </form>
          <ErrorNotice error={target.error} />
          {target.loading && <Loading />}
          {target.data && (
            <p>
              {target.data.display_name} · {target.data.id} ·{" "}
              {t(target.data.status === "active" ? "Enabled" : "Disabled")}
            </p>
          )}
          <form
            onSubmit={(event) => {
              event.preventDefault();
              void submit();
            }}
          >
            <Field label={t("Reason")}>
              <textarea
                required
                maxLength={500}
                value={reason}
                disabled={mutation.pending}
                onChange={(event) => setReason(event.target.value)}
              />
            </Field>
            <label className="check">
              <input
                type="checkbox"
                checked={confirmed}
                disabled={mutation.pending}
                onChange={(event) => setConfirmed(event.target.checked)}
              />
              {t("I have checked the customer, target agency and impact.")}
            </label>
            <ErrorNotice error={error} />
            <button
              disabled={
                mutation.pending ||
                !confirmed ||
                !reason.trim() ||
                !target.data ||
                target.data.status !== "active" ||
                String(target.data.id) === customer.agency_id
              }
            >
              {t(transfer ? "Transfer customer" : "Bind existing customer")}
            </button>
          </form>
        </>
      )}
    </section>
  );
}

function ProvisioningProgress(props: { id: string; onChanged: () => void }) {
  const { t } = useTranslation();
  const query = useQuery<ProvisioningJob>(`/root/provisioning/${props.id}`);
  const mutation = useMutation();
  const [reason, setReason] = useState("");
  const [error, setError] = useState<unknown>(null);
  const active = query.data && ["queued", "processing", "blocked"].includes(query.data.status);
  useEffect(() => {
    if (!active) return;
    const timer = window.setInterval(query.reload, 3000);
    return () => window.clearInterval(timer);
  }, [active, query.reload]);
  async function cancel() {
    setError(null);
    try {
      await mutation.mutate(
        `/root/provisioning/${props.id}/cancel`,
        { reason },
        {
          action: "provisioning.cancel",
          objectId: `provisioning:${props.id}`,
          title: t("Cancel binding"),
        },
      );
      query.reload();
      props.onChanged();
    } catch (cause) {
      setError(cause);
    }
  }
  return (
    <section>
      <h4>
        {t("Binding job")} #{props.id}
      </h4>
      <ErrorNotice error={query.error || error} />
      {query.data && (
        <>
          <p role="status">
            {t("Status")}: {t(query.data.status)}
          </p>
          <p>{query.data.block_reason || query.data.cancel_reason}</p>
          <DataTable
            rows={query.data.blocking_tasks}
            columns={[
              { key: "task_id", label: "Task ID" },
              { key: "status", label: "Status" },
              { key: "progress", label: "Progress" },
            ]}
          />
          <button
            className="secondary"
            type="button"
            onClick={() => {
              query.reload();
              props.onChanged();
            }}
          >
            {t("Refresh")}
          </button>
          {active && (
            <form
              onSubmit={(event) => {
                event.preventDefault();
                void cancel();
              }}
            >
              <Field label={t("Cancellation reason")}>
                <input
                  required
                  maxLength={500}
                  value={reason}
                  disabled={mutation.pending}
                  onChange={(event) => setReason(event.target.value)}
                />
              </Field>
              <button className="danger" disabled={mutation.pending || !reason.trim()}>
                {t("Cancel binding")}
              </button>
            </form>
          )}
        </>
      )}
    </section>
  );
}
