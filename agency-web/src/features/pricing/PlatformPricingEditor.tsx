import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { ActionIcon } from "../../components/Heading";
import { ErrorNotice, Field, Loading } from "../../components/ui";
import { useQuery } from "../../lib/client";
import { useMutation } from "../../lib/mutations";
import { formatCoefficient, parseCoefficient } from "./policy";
import type { PlatformPriceRow, PlatformPricing } from "./types";

type PlatformDraft = Record<
  string,
  { platformCost: string; agencyCost: string; defaultSales: string }
>;

export function PlatformPricingEditor() {
  const pricing = useQuery<PlatformPricing>("/root/platform-pricing");
  if (pricing.loading) return <Loading />;
  if (!pricing.data) return <ErrorNotice error={pricing.error} />;
  return <PlatformPricingForm key={`${pricing.data.revision}:${pricing.data.refreshed_at_ms}`} data={pricing.data} reload={pricing.reload} />;
}

function PlatformPricingForm(props: { data: PlatformPricing; reload: () => void }) {
  const { t } = useTranslation();
  const mutation = useMutation();
  const [search, setSearch] = useState("");
  const [reason, setReason] = useState("");
  const [error, setError] = useState<unknown>(null);
  const [draft, setDraft] = useState<PlatformDraft>(() =>
    Object.fromEntries(
      props.data.items.map((row) => [
        row.origin_model_name,
        {
          platformCost: coefficientValue(row.platform_cost_bps),
          agencyCost: coefficientValue(row.agency_cost_bps),
          defaultSales: coefficientValue(row.default_sales_bps),
        },
      ]),
    ),
  );
  const visible = useMemo(() => {
    const query = search.trim().toLowerCase();
    if (!query) return props.data.items;
    return props.data.items.filter(
      (row) =>
        row.origin_model_name.toLowerCase().includes(query) ||
        row.channel_names.some((name) => name.toLowerCase().includes(query)),
    );
  }, [props.data.items, search]);

  function update(model: string, key: keyof PlatformDraft[string], value: string) {
    setDraft((current) => ({ ...current, [model]: { ...current[model], [key]: value } }));
  }

  async function publish() {
    setError(null);
    try {
      const modelPrices = props.data.items.flatMap((row) => {
        const value = draft[row.origin_model_name];
        const values = [value.platformCost, value.agencyCost, value.defaultSales];
        if (values.every((item) => item === "")) return [];
        if (values.some((item) => item === "")) {
          throw new Error("Complete all three coefficients for a configured model.");
        }
        const platformCost = parseCoefficient(value.platformCost);
        const agencyCost = parseCoefficient(value.agencyCost);
        const defaultSales = parseCoefficient(value.defaultSales);
        if (agencyCost < platformCost) {
          throw new Error(t("Agency cost coefficient cannot be lower than platform cost coefficient. Model: {{model}}", { model: row.origin_model_name }));
        }
        if (defaultSales < agencyCost) {
          throw new Error(t("Sales coefficient cannot be lower than agency cost coefficient. Model: {{model}}", { model: row.origin_model_name }));
        }
        return [{
          origin_model_name: row.origin_model_name,
          platform_cost_bps: platformCost,
          agency_cost_bps: agencyCost,
          default_sales_bps: defaultSales,
        }];
      });
      await mutation.mutate(
        "/root/platform-pricing/publish",
        { expected_revision: props.data.revision, model_prices: modelPrices, reason },
        { action: "pricing.platform.publish", objectId: "platform_pricing:current" },
      );
      props.reload();
    } catch (cause) {
      setError(cause);
    }
  }

  return (
    <section className="pricing-matrix-card">
      <div className="toolbar pricing-toolbar">
        <div>
          <h3>{t("Platform model coefficient matrix")}</h3>
          <p className="muted">
            {t("Model and channel data refresh from enabled platform routes. Blank rows are not configured.")}
          </p>
        </div>
        <button className="secondary button-icon" type="button" onClick={props.reload}>
          <ActionIcon name="refresh" />
          {t("Refresh")}
        </button>
      </div>
      <div className="pricing-search">
        <Field label={t("Search models or channels")}>
          <input value={search} onChange={(event) => setSearch(event.target.value)} />
        </Field>
        <span className="pricing-live-badge">{t("Live platform data")} · {visible.length}</span>
      </div>
      <div className="table-wrap pricing-matrix-wrap">
        <table className="pricing-matrix">
          <thead>
            <tr>
              <th>{t("Model name")}</th>
              <th>{t("Channel name")}</th>
              <th>{t("Platform cost coefficient")}</th>
              <th>{t("Agency cost coefficient")}</th>
              <th>{t("Sales coefficient")}</th>
            </tr>
          </thead>
          <tbody>
            {visible.map((row) => (
              <PlatformPricingRow key={row.origin_model_name} row={row} value={draft[row.origin_model_name]} update={update} />
            ))}
          </tbody>
        </table>
      </div>
      {props.data.items.length === 0 && (
        <p className="empty">{t("No enabled models or channels are currently available.")}</p>
      )}
      <div className="pricing-publish-row">
        <Field label={t("Change reason")}>
          <input value={reason} maxLength={1000} onChange={(event) => setReason(event.target.value)} />
        </Field>
        <button className="button-icon" type="button" disabled={mutation.pending || !reason.trim()} onClick={() => void publish()}>
          <ActionIcon name="save" />
          {t("Publish platform pricing")}
        </button>
      </div>
      <ErrorNotice error={error} />
    </section>
  );
}

function PlatformPricingRow(props: {
  row: PlatformPriceRow;
  value: PlatformDraft[string];
  update: (model: string, key: keyof PlatformDraft[string], value: string) => void;
}) {
  const { t } = useTranslation();
  const value = props.value ?? { platformCost: "", agencyCost: "", defaultSales: "" };
  return (
    <tr>
      <td><strong>{props.row.origin_model_name}</strong></td>
      <td>
        <div className="channel-tags">
          {props.row.channel_names.length
            ? props.row.channel_names.map((name) => <span key={name}>{name}</span>)
            : <span>{t("Channel unavailable")}</span>}
        </div>
      </td>
      <CoefficientInput label={t("Platform cost coefficient")} model={props.row.origin_model_name} value={value.platformCost} placeholder="0.5000" onChange={(value) => props.update(props.row.origin_model_name, "platformCost", value)} />
      <CoefficientInput label={t("Agency cost coefficient")} model={props.row.origin_model_name} value={value.agencyCost} placeholder="0.5500" onChange={(value) => props.update(props.row.origin_model_name, "agencyCost", value)} />
      <CoefficientInput label={t("Sales coefficient")} model={props.row.origin_model_name} value={value.defaultSales} placeholder="0.6000" onChange={(value) => props.update(props.row.origin_model_name, "defaultSales", value)} />
    </tr>
  );
}

function CoefficientInput(props: { label: string; model: string; value: string; placeholder: string; onChange: (value: string) => void }) {
  return (
    <td>
      <input aria-label={`${props.label}: ${props.model}`} inputMode="decimal" placeholder={props.placeholder} value={props.value} onChange={(event) => props.onChange(event.target.value)} />
    </td>
  );
}

function coefficientValue(value: number | null): string {
  return value == null ? "" : formatCoefficient(value);
}
