import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Dialog, ErrorNotice, Field } from "../../components/ui";

export function PublishReasonDialog(props: {
  title: string;
  busy: boolean;
  error?: unknown;
  onClose: () => void;
  onConfirm: (reason: string) => void;
}) {
  const { t } = useTranslation();
  const [reason, setReason] = useState("");
  return <Dialog title={props.title} onClose={props.onClose} busy={props.busy}>
    <Field label={t("Change reason (optional)")}><input autoFocus maxLength={2000} value={reason} onChange={(event) => setReason(event.target.value)} /></Field>
    <ErrorNotice error={props.error} />
    <div className="actions dialog-actions">
      <button type="button" className="secondary" disabled={props.busy} onClick={props.onClose}>{t("Cancel")}</button>
      <button type="button" disabled={props.busy} onClick={() => props.onConfirm(reason.trim())}>{props.title}</button>
    </div>
  </Dialog>;
}
