import { useState } from "react";
import { useTranslation } from "react-i18next";
import { api, useQuery } from "../../lib/client";
import { useMutation } from "../../lib/mutations";
import { ErrorNotice, Field, Loading, Pager, Table, Time } from "../../components/ui";
import { PolicyFields } from "./PolicyFields";
import { PolicyComparison } from "./PolicyComparison";
import { Preview } from "./Preview";
import { draftToPolicy, policyToDraft, pricingRequest } from "./policy";
import type { Policy, PricePreview, PricingHistory } from "./types";
import type { Agency, AgencyList } from "../agencies/types";

export function PricingPage(props: { root: boolean; agencyId: string | null }) {
  const { t } = useTranslation();
  const [selected, setSelected] = useState(props.agencyId ?? "");
  const [cursor, setCursor] = useState("");
  const agencies = useQuery<AgencyList>(
    props.root ? "/root/agencies?limit=200&cursor=" + encodeURIComponent(cursor) : null,
  );
  const agencyId = props.root ? selected : props.agencyId;
  return (
    <>
      {props.root && (
        <div className="toolbar">
          <Field label={t("Select an agency")}>
            <select value={selected} onChange={(e) => setSelected(e.target.value)}>
              <option value="">{t("Select an agency")}</option>
              {selected && !agencies.data?.items.some((row) => String(row.id) === selected) && (
                <option value={selected}>{selected}</option>
              )}
              {(agencies.data?.items ?? []).map((row) => (
                <option key={row.id} value={String(row.id)}>
                  {row.display_name}
                </option>
              ))}
            </select>
          </Field>
          <Pager
            nextCursor={agencies.data?.meta?.next_cursor}
            hasPrevious={Boolean(cursor)}
            onNext={() => setCursor(agencies.data?.meta?.next_cursor ?? "")}
            onReset={() => setCursor("")}
          />
          <ErrorNotice error={agencies.error} />
        </div>
      )}
      {props.root && !agencyId ? (
        <p className="empty">{t("Select an agency to view and edit its prices.")}</p>
      ) : (
        <AgencyPricing key={agencyId ?? "own"} root={props.root} agencyId={agencyId} />
      )}
    </>
  );
}

function AgencyPricing(props: { root: boolean; agencyId: string | null }) {
  const { t } = useTranslation();
  const path = props.root ? `/root/agencies/${props.agencyId}/pricing` : "/pricing";
  const pricing = useQuery<Policy>(path);
  const agency = useQuery<Agency>(props.root ? `/root/agencies/${props.agencyId}` : null);
  const history = useQuery<{ items: PricingHistory[] }>(path + "/history?limit=200");
  if (pricing.loading) return <Loading />;
  if (!pricing.data)
    return (
      <>
        <ErrorNotice error={pricing.error} />
        <button type="button" onClick={pricing.reload}>
          {t("Refresh")}
        </button>
      </>
    );
  return (
    <>
      <ErrorNotice error={pricing.error ?? agency.error} />
      <PricingEditor
        key={String(pricing.data.agency_id) + ":" + pricing.data.revision}
        root={props.root}
        policy={pricing.data}
        path={path}
        disabled={agency.data?.status === "disabled"}
        onSaved={() => {
          pricing.reload();
          history.reload();
          agency.reload();
        }}
        history={history.data?.items ?? []}
      />
      <section>
        <h3>{t("Price history")}</h3>
        <ErrorNotice error={history.error} />
        <Table
          rows={history.data?.items ?? []}
          rowKey={(row) => String(row.policy_version_id)}
          columns={[
            { key: "revision", label: t("Revision") },
            {
              key: "created_at_ms",
              label: t("Published at"),
              render: (row) => <Time value={row.created_at_ms} />,
            },
            { key: "reason", label: t("Reason") },
          ]}
        />
      </section>
    </>
  );
}

function PricingEditor(props: {
  root: boolean;
  policy: Policy;
  path: string;
  disabled: boolean;
  onSaved: () => void;
  history: PricingHistory[];
}) {
  const { t } = useTranslation();
  const [draft, setDraft] = useState(() => policyToDraft(props.policy));
  const [reason, setReason] = useState("");
  const [error, setError] = useState<unknown>(null);
  const [preview, setPreview] = useState<PricePreview | null>(null);
  const [previewing, setPreviewing] = useState(false);
  const [published, setPublished] = useState<{ revision: number; committed_at_ms: number } | null>(
    null,
  );
  const mutation = useMutation();
  let candidate: Policy | null = null;
  let validation: string | null = null;
  try {
    candidate = draftToPolicy(draft, props.policy, props.root);
  } catch (cause) {
    validation = cause instanceof Error ? t(cause.message) : t("Invalid pricing.");
  }

  async function showPreview() {
    if (!candidate) return;
    setPreviewing(true);
    setError(null);
    try {
      const path = props.root ? props.path + "/preview" : "/pricing/sales/preview";
      setPreview(
        await api<PricePreview>(path, {
          method: "POST",
          body: JSON.stringify(pricingRequest(candidate, props.root, reason)),
        }),
      );
    } catch (cause) {
      setError(cause);
    } finally {
      setPreviewing(false);
    }
  }

  async function publish() {
    if (!candidate) return;
    setError(null);
    try {
      const path = props.root ? props.path + "/publish" : "/pricing/sales/publish";
      const result = await mutation.mutate<{ revision: number; committed_at_ms: number }>(
        path,
        pricingRequest(candidate, props.root, reason),
        {
          action: props.root ? "pricing.root.publish" : "pricing.sales.publish",
          objectId: `agency:${props.policy.agency_id}`,
        },
      );
      setPublished(result);
    } catch (cause) {
      setError(cause);
    }
  }

  return (
    <section>
      <div className="toolbar">
        <h3>
          {t("Revision")} {props.policy.revision}
        </h3>
        <button
          type="button"
          className="secondary"
          disabled={mutation.pending}
          onClick={() => {
            if (window.confirm(t("Reload prices and discard this draft?"))) props.onSaved();
          }}
        >
          {t("Reload current prices")}
        </button>
      </div>
      {props.disabled && (
        <p className="notice">
          {t("This agency is disabled. You can preview a draft, but publishing is unavailable.")}
        </p>
      )}
      {published && (
        <div role="status" className="notice">
          {t("Price published")} · {t("Revision")} {published.revision} ·{" "}
          <Time value={published.committed_at_ms} />{" "}
          <button type="button" onClick={props.onSaved}>
            {t("Load published version")}
          </button>
        </div>
      )}
      <PolicyFields
        root={props.root}
        draft={draft}
        disabled={mutation.pending || previewing || Boolean(published)}
        onChange={(value) => {
          setDraft(value);
          setPreview(null);
        }}
      />
      <Field label={t("Reason")}>
        <input
          value={reason}
          maxLength={1000}
          onChange={(e) => setReason(e.target.value)}
          disabled={mutation.pending || Boolean(published)}
        />
      </Field>
      <Field label={t("Copy a historical version into the draft")}>
        <select
          value=""
          disabled={mutation.pending || Boolean(published)}
          onChange={(e) => {
            const row = props.history.find(
              (item) => String(item.policy_version_id) === e.target.value,
            );
            if (
              row &&
              window.confirm(t("Replace this draft with the selected historical prices?"))
            ) {
              const historical = policyToDraft(row.policy);
              if (!props.root) {
                historical.settlement = draft.settlement;
                historical.spread = draft.spread;
                historical.cap = draft.cap;
              }
              setDraft(historical);
              setPreview(null);
            }
          }}
        >
          <option value="">{t("Select a historical revision")}</option>
          {props.history.map((row) => (
            <option key={row.policy_version_id} value={String(row.policy_version_id)}>
              {t("Revision")} {row.revision} · {row.reason}
            </option>
          ))}
        </select>
      </Field>
      <p className="muted">
        {t(
          "Historical prices are copied into a new revision. Current prices stay active until publication.",
        )}
      </p>
      {validation && (
        <p role="alert" className="error">
          {validation}
        </p>
      )}
      {candidate && <PolicyComparison before={props.policy} after={candidate} />}
      {preview && candidate && <Preview data={preview} policy={candidate} />}
      <ErrorNotice error={error} />
      <div className="toolbar">
        <button
          type="button"
          className="secondary"
          disabled={!candidate || previewing || mutation.pending || Boolean(published)}
          onClick={() => void showPreview()}
        >
          {t("Preview prices")}
        </button>
        <button
          type="button"
          disabled={
            !candidate || !reason.trim() || props.disabled || mutation.pending || Boolean(published)
          }
          onClick={() => void publish()}
        >
          {t("Publish prices")}
        </button>
      </div>
      <p className="muted">
        {t("A revision conflict keeps your draft. Reload current prices before publishing again.")}
      </p>
    </section>
  );
}
