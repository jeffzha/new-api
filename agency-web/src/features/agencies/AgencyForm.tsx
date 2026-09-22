import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ActionIcon, PageHeading } from "../../components/Heading";
import { useMutation } from "../../lib/mutations";
import { ErrorNotice, Field } from "../../components/ui";
import type { PasswordDelivery } from "./types";

export function AgencyForm(props: {
  onCreated: (delivery: PasswordDelivery) => void;
  onCancel: () => void;
}) {
  const { t } = useTranslation();
  const [name, setName] = useState("");
  const [username, setUsername] = useState("");
  const [error, setError] = useState<unknown>(null);
  const mutation = useMutation();
  async function create() {
    setError(null);
    try {
      const body = {
        display_name: name.trim(),
        operator_username: username.trim(),
        status: "active",
      };
      props.onCreated(
        await mutation.mutate<PasswordDelivery>("/root/agencies", body, {
          action: "agency.create",
          objectId: "agency:new",
        }),
      );
    } catch (cause) {
      setError(cause);
    }
  }
  return (
    <section className="panel agency-form-panel">
      <div className="agency-form-heading">
        <PageHeading icon="agency" level={3}>{t("Create agency")}</PageHeading>
        <button
          type="button"
          className="secondary button-icon"
          disabled={mutation.pending}
          onClick={props.onCancel}
        >
          <ActionIcon name="close" />
          {t("Cancel")}
        </button>
      </div>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          void create();
        }}
      >
        <fieldset disabled={mutation.pending}>
          <div className="form-grid">
            <Field label={t("Agency name")}>
              <input
                value={name}
                maxLength={191}
                onChange={(e) => setName(e.target.value)}
                required
              />
            </Field>
            <Field label={t("Operator username")}>
              <input
                autoComplete="off"
                value={username}
                maxLength={191}
                onChange={(e) => setUsername(e.target.value)}
                required
              />
            </Field>
          </div>
          <p className="muted">{t("Pricing is inherited from the platform pricing policy after the agency is created.")}</p>
        </fieldset>
        <ErrorNotice error={error} />
        <div className="toolbar">
          <button className="button-icon" type="submit" disabled={mutation.pending || !name.trim() || !username.trim()}>
            <ActionIcon name="check" />
            {t("Create agency")}
          </button>
        </div>
      </form>
    </section>
  );
}
