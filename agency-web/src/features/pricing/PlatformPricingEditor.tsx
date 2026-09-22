import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { ActionIcon } from "../../components/Heading";
import { ErrorNotice, Field, Loading, Pager } from "../../components/ui";
import { useQuery } from "../../lib/client";
import { useMutation } from "../../lib/mutations";
import { formatCoefficient, parseCoefficient } from "./policy";
import type { PlatformPriceRow, PlatformPricing } from "./types";

type PlatformDraft = Record<
  string,
  { channelCosts: Record<string, string>; agencyCost: string; defaultSales: string }
>;

type PublishedPlatformModelPrice = {
  origin_model_name: string;
  platform_cost_bps?: number;
  channel_costs?: { channel_id: number; platform_cost_bps: number }[];
  agency_cost_bps: number;
  default_sales_bps: number;
};

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
  const [page, setPage] = useState(0);
  const [reason, setReason] = useState("");
  const [error, setError] = useState<unknown>(null);
  const [draft, setDraft] = useState<PlatformDraft>(() =>
    Object.fromEntries(
      props.data.items.map((row) => [
        row.origin_model_name,
        {
          channelCosts: Object.fromEntries(
            row.channel_costs.map((channel) => [
              String(channel.channel_id),
              coefficientValue(channel.platform_cost_bps ?? row.platform_cost_bps),
            ]),
          ),
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
  const pageSize = 20;
  const pageCount = Math.max(1, Math.ceil(visible.length / pageSize));
  const currentPage = Math.min(page, pageCount - 1);
  const pageRows = visible.slice(currentPage * pageSize, (currentPage + 1) * pageSize);

  function update(model: string, key: "agencyCost" | "defaultSales", value: string) {
    setDraft((current) => ({ ...current, [model]: { ...current[model], [key]: value } }));
  }

  function updateChannelCost(model: string, channelID: number, value: string) {
    setDraft((current) => ({
      ...current,
      [model]: {
        ...current[model],
        channelCosts: { ...current[model].channelCosts, [String(channelID)]: value },
      },
    }));
  }

  async function publish() {
    setError(null);
    try {
      const modelPrices: PublishedPlatformModelPrice[] = props.data.items.flatMap<PublishedPlatformModelPrice>((row) => {
        const value = draft[row.origin_model_name];
        if (row.channel_costs.length === 0) {
          const values = [coefficientValue(row.platform_cost_bps), value.agencyCost, value.defaultSales];
          if (values.every((item) => item === "")) return [];
          if (values.some((item) => item === "")) {
            throw new Error(t("Complete the platform cost, agency cost, and sales coefficient for a configured model."));
          }
          const platformCost = parseCoefficient(coefficientValue(row.platform_cost_bps));
          const agencyCost = parseCoefficient(value.agencyCost);
          const defaultSales = parseCoefficient(value.defaultSales);
          if (defaultSales < agencyCost) {
            throw new Error(t("Sales coefficient cannot be lower than agency cost coefficient. Model: {{model}}", { model: row.origin_model_name }));
          }
          return [{
            origin_model_name: row.origin_model_name,
            platform_cost_bps: platformCost,
            agency_cost_bps: agencyCost,
            default_sales_bps: defaultSales,
          }];
        }
        const channelValues = row.channel_costs.map((channel) => value.channelCosts[String(channel.channel_id)] ?? "");
        const values = [...channelValues, value.agencyCost, value.defaultSales];
        if (values.every((item) => item === "")) return [];
        if (values.some((item) => item === "")) {
          throw new Error(t("Complete every enabled channel cost, agency cost, and sales coefficient for a configured model."));
        }
        const channelCosts = row.channel_costs.map((channel) => ({
          channel_id: channel.channel_id,
          platform_cost_bps: parseCoefficient(value.channelCosts[String(channel.channel_id)]),
        }));
        const agencyCost = parseCoefficient(value.agencyCost);
        const defaultSales = parseCoefficient(value.defaultSales);
        if (defaultSales < agencyCost) {
          throw new Error(t("Sales coefficient cannot be lower than agency cost coefficient. Model: {{model}}", { model: row.origin_model_name }));
        }
        return [{
          origin_model_name: row.origin_model_name,
          channel_costs: channelCosts,
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
          <input value={search} onChange={(event) => { setSearch(event.target.value); setPage(0); }} />
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
            {pageRows.map((row) => (
              <PlatformPricingRow key={row.origin_model_name} row={row} value={draft[row.origin_model_name]} update={update} updateChannelCost={updateChannelCost} />
            ))}
          </tbody>
        </table>
      </div>
      {visible.length > pageSize && (
        <Pager
          nextCursor={currentPage < pageCount - 1 ? String(currentPage + 1) : undefined}
          hasPrevious={currentPage > 0}
          onNext={() => setPage((value) => Math.min(value + 1, pageCount - 1))}
          onReset={() => setPage(0)}
        />
      )}
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
  update: (model: string, key: "agencyCost" | "defaultSales", value: string) => void;
  updateChannelCost: (model: string, channelID: number, value: string) => void;
}) {
  const { t } = useTranslation();
  const value = props.value ?? { channelCosts: {}, agencyCost: "", defaultSales: "" };
  return (
    <tr>
      <td><strong>{props.row.origin_model_name}</strong></td>
      <td>
        <div className="channel-tags channel-cost-list">
          {props.row.channel_costs.length
            ? props.row.channel_costs.map((channel) => <span key={channel.channel_id}>{channel.channel_name}</span>)
            : <span>{t("Channel unavailable")}</span>}
        </div>
      </td>
      <td>
        <div className="channel-cost-list">
          {props.row.channel_costs.map((channel) => (
            <input
              key={channel.channel_id}
              aria-label={`${t("Platform cost coefficient")}: ${props.row.origin_model_name} / ${channel.channel_name}`}
              inputMode="decimal"
              placeholder="0.5000"
              value={value.channelCosts[String(channel.channel_id)] ?? ""}
              onChange={(event) => props.updateChannelCost(props.row.origin_model_name, channel.channel_id, event.target.value)}
            />
          ))}
        </div>
      </td>
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
