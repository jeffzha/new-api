import { useState } from "react";
import { useTranslation } from "react-i18next";
import { api } from "../../lib/client";
import { useMutation } from "../../lib/mutations";
import { ErrorNotice, Field } from "../../components/ui";
import { PolicyFields } from "../pricing/PolicyFields";
import { Preview } from "../pricing/Preview";
import { draftToPolicy, initialPolicy, policyToDraft, pricingRequest } from "../pricing/policy";
import type { Policy, PricePreview } from "../pricing/types";
import type { PasswordDelivery } from "./types";

export function AgencyForm(props: {
  onCreated: (delivery: PasswordDelivery) => void;
  onCancel: () => void;
}) {
  const { t } = useTranslation();
  const [name, setName] = useState("");
  const [username, setUsername] = useState("");
  const [draft, setDraft] = useState(() => policyToDraft(initialPolicy));
  const [preview, setPreview] = useState<(PricePreview & { policy: Policy }) | null>(null);
  const [previewing, setPreviewing] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const mutation = useMutation();
  function currentPolicy() {
    try {
      return draftToPolicy(draft, initialPolicy, true);
    } catch (cause) {
      throw new Error(cause instanceof Error ? t(cause.message) : t("Invalid pricing."));
    }
  }
  async function previewPrices() {
    setError(null);
    setPreviewing(true);
    try {
      // Preview validates a candidate policy only; it does not load the agency ID.
      const policy = currentPolicy();
      const result = await api<PricePreview>("/root/agencies/0/pricing/preview", {
        method: "POST",
        body: JSON.stringify(pricingRequest(policy, true, "")),
      });
      setPreview({ ...result, policy });
    } catch (cause) {
      setError(cause);
    } finally {
      setPreviewing(false);
    }
  }
  async function create() {
    setError(null);
    try {
      const pricing = currentPolicy();
      const body = {
        display_name: name.trim(),
        operator_username: username.trim(),
        status: "active",
        pricing,
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
    <section className="panel">
      <h3>{t("Create agency")}</h3>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          void create();
        }}
      >
        <fieldset disabled={mutation.pending || previewing}>
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
          <PolicyFields
            draft={draft}
            root
            onChange={(value) => {
              setDraft(value);
              setPreview(null);
            }}
          />
          <p className="muted">
            {t(
              "The agency starts active. Its account, invitation, and initial price policy are created together.",
            )}
          </p>
        </fieldset>
        {preview && <Preview data={preview} policy={preview.policy} />}
        <ErrorNotice error={error} />
        <div className="toolbar">
          <button
            type="button"
            className="secondary"
            disabled={mutation.pending || previewing}
            onClick={() => void previewPrices()}
          >
            {t("Preview prices")}
          </button>
          <button type="submit" disabled={mutation.pending || !name.trim() || !username.trim()}>
            {t("Create agency")}
          </button>
          <button
            type="button"
            className="secondary"
            disabled={mutation.pending}
            onClick={props.onCancel}
          >
            {t("Cancel")}
          </button>
        </div>
      </form>
    </section>
  );
}
