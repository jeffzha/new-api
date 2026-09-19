import { useState } from "react";
import { useTranslation } from "react-i18next";
import { PageHeader } from "../../components/PageHeader";
import { ActionIcon } from "../../components/Heading";
import { useQuery } from "../../lib/client";
import { useMutation } from "../../lib/mutations";
import { ErrorNotice, Field, Loading } from "../../components/ui";
import type { Agency, PasswordDelivery } from "./types";

export function AgencyEditor(props: {
  id: string;
  onSaved: () => void;
  onDelivery: (delivery: PasswordDelivery) => void;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const query = useQuery<Agency>(`/root/agencies/${props.id}`);
  if (query.loading) return <Loading />;
  if (!query.data)
    return (
      <>
        <ErrorNotice error={query.error} />
        <button className="secondary button-icon" type="button" onClick={query.reload}>
          <ActionIcon name="refresh" />
          {t("Refresh")}
        </button>
      </>
    );
  return (
    <>
      <ErrorNotice error={query.error} />
      <AgencyDetails
        key={props.id + ":" + query.data.version}
        agency={query.data}
        onSaved={() => {
          props.onSaved();
          query.reload();
        }}
        onDelivery={props.onDelivery}
        onClose={props.onClose}
      />
    </>
  );
}

function AgencyDetails(props: {
  agency: Agency;
  onSaved: () => void;
  onDelivery: (delivery: PasswordDelivery) => void;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const [name, setName] = useState(props.agency.display_name);
  const [reason, setReason] = useState("");
  const [error, setError] = useState<unknown>(null);
  const mutation = useMutation();
  const path = `/root/agencies/${props.agency.id}`;
  const objectId = `agency:${props.agency.id}`;
  const disabled = props.agency.status === "disabled";
  async function save() {
    setError(null);
    try {
      await mutation.mutate(
        path,
        { display_name: name.trim(), expected_version: props.agency.version },
        { method: "PATCH", action: "agency.update", objectId },
      );
      props.onSaved();
    } catch (cause) {
      setError(cause);
    }
  }
  async function changeStatus() {
    setError(null);
    const action = disabled ? "enable" : "disable";
    try {
      await mutation.mutate(
        path + "/" + action,
        { expected_version: props.agency.version, reason: reason.trim() },
        { action: "agency." + action, objectId },
      );
      props.onSaved();
    } catch (cause) {
      setError(cause);
    }
  }
  async function resetPassword() {
    setError(null);
    try {
      const delivery = await mutation.mutate<PasswordDelivery>(
        path + "/reset-password",
        {},
        { action: "agency.reset_password", objectId },
      );
      props.onDelivery({ ...delivery, agency: props.agency });
    } catch (cause) {
      setError(cause);
    }
  }
  return (
    <section className="panel">
      <PageHeader
        icon="agency"
        title={t("Edit agency") + " · " + props.agency.display_name}
        actions={<button className="secondary button-icon" type="button" disabled={mutation.pending} onClick={props.onClose}>
          <ActionIcon name="close" />
          {t("Close")}
        </button>}
      />
      <p>
        {t("Operator username")}: {props.agency.operator_username} · {t("Version")}:{" "}
        {props.agency.version}
      </p>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          void save();
        }}
      >
        <Field label={t("Agency name")}>
          <input
            value={name}
            maxLength={191}
            onChange={(e) => setName(e.target.value)}
            required
            disabled={mutation.pending}
          />
        </Field>
        <div className="toolbar">
          <button className="button-icon" type="submit" disabled={mutation.pending || !name.trim()}>
            <ActionIcon name="save" />
            {t("Save agency")}
          </button>
        </div>
      </form>
      <h4>{t("Agency access")}</h4>
      <p className="notice">
        {t(
          "Disabling blocks new invitations and operator sessions, and holds unpaid withdrawals. Existing customer API access remains available.",
        )}
      </p>
      {disabled && (
        <p>
          {t(
            "Enabling restores the previous price policy for future requests. Past commission is not recalculated.",
          )}
        </p>
      )}
      <Field label={t("Reason")}>
        <input
          value={reason}
          onChange={(e) => setReason(e.target.value)}
          maxLength={1000}
          disabled={mutation.pending}
        />
      </Field>
      <div className="agency-status-actions">
        <button
          className={disabled ? "secondary button-icon" : "danger button-icon"}
          type="button"
          disabled={mutation.pending || !reason.trim()}
          onClick={() => void changeStatus()}
        >
          <ActionIcon name={disabled ? "check" : "ban"} />
          {t(disabled ? "Enable agency" : "Disable agency")}
        </button>
      </div>
      <h4>{t("Reset operator password")}</h4>
      <p className="muted">
        {t(
          "Resetting invalidates operator sessions and requires a password change at the next sign-in.",
        )}
      </p>
      <button
        className="secondary button-icon"
        type="button"
        disabled={mutation.pending}
        onClick={() => void resetPassword()}
      >
        <ActionIcon name="refresh" />
        {t("Reset operator password")}
      </button>
      <ErrorNotice error={error} />
      {Boolean(error) && (
        <button
          type="button"
          className="secondary button-icon"
          disabled={mutation.pending}
          onClick={props.onSaved}
        >
          <ActionIcon name="refresh" />
          {t("Reload current agency")}
        </button>
      )}
    </section>
  );
}
