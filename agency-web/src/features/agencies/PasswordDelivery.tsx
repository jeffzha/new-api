import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Field, ErrorNotice, Time } from "../../components/ui";
import { useMutation } from "../../lib/mutations";
import type { PasswordDelivery as Delivery } from "./types";

export function PasswordDelivery(props: { delivery: Delivery; onClose: () => void }) {
  const { t } = useTranslation();
  const [password, setPassword] = useState(props.delivery.temporary_password ?? "");
  const [acknowledged, setAcknowledged] = useState(false);
  const [copied, setCopied] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const mutation = useMutation();
  async function acknowledge() {
    setError(null);
    try {
      const result = await mutation.mutate<{ temporary_password: string }>(
        `/root/deliveries/${props.delivery.delivery_id}/ack`,
        { operation_id: props.delivery.delivery_operation_id },
      );
      setPassword(result.temporary_password || password);
      setAcknowledged(true);
    } catch (cause) {
      setError(cause);
    }
  }
  async function copyPassword() {
    setError(null);
    try {
      await navigator.clipboard.writeText(password);
      setCopied(true);
    } catch (cause) {
      setError(cause);
    }
  }
  return (
    <section className="panel" aria-label={t("Temporary password delivery")}>
      <h3>{t("Temporary password delivery")}</h3>
      <p className="notice">
        {t(
          "Copy this temporary password and confirm receipt. The operator must change it at first sign-in.",
        )}
      </p>
      {props.delivery.agency?.operator_username && (
        <p>
          {t("Operator username")}: {props.delivery.agency.operator_username}
        </p>
      )}
      <Field label={t("Temporary password")}>
        <input value={password} readOnly autoComplete="off" />
      </Field>
      {props.delivery.temporary_password_expires_at && (
        <p>
          {t("Delivery expires at")}: <Time value={props.delivery.temporary_password_expires_at} />
        </p>
      )}
      {props.delivery.invite_url && (
        <Field label={t("Invitation link")}>
          <input readOnly value={props.delivery.invite_url} />
        </Field>
      )}
      <ErrorNotice error={error} />
      {acknowledged && (
        <p role="status">{t("Receipt confirmed. The server copy has been destroyed.")}</p>
      )}
      <div className="toolbar">
        <button
          type="button"
          className="secondary"
          disabled={!password}
          onClick={() => void copyPassword()}
        >
          {t(copied ? "Copied" : "Copy password")}
        </button>
        <button
          type="button"
          disabled={acknowledged || mutation.pending}
          onClick={() => void acknowledge()}
        >
          {t(password ? "Confirm receipt" : "Retrieve temporary password")}
        </button>
        <button
          type="button"
          className="secondary"
          disabled={!acknowledged || mutation.pending}
          onClick={() => {
            setPassword("");
            props.onClose();
          }}
        >
          {t("Close")}
        </button>
      </div>
    </section>
  );
}
