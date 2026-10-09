import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { ActionIcon } from "../../components/Heading";
import { ErrorNotice, Field, Loading, Pager } from "../../components/ui";
import { useQuery } from "../../lib/client";
import { useMutation } from "../../lib/mutations";
import { CoefficientAdjustDialog, type AdjustmentOutcome } from "./AdjustDialog";
import { ChannelFilter, ChannelTags, type ChannelFilterOption } from "./ChannelFilter";
import {
  formatCoefficient,
  parseCoefficient,
  type CoefficientAdjustmentError,
  type CoefficientAdjustmentRow,
} from "./policy";
import type { PlatformPriceRow, PlatformPricing } from "./types";

type PlatformDraft = Record<
  string,
  { channelCosts: Record<string, string>; agencyCost: string; defaultSales: string }
>;

type AdjustTarget = "agencyCost" | "defaultSales";

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
  const [channels, setChannels] = useState<string[]>([]);
  const [selected, setSelected] = useState<string[]>([]);
  const [adjustTarget, setAdjustTarget] = useState<AdjustTarget | null>(null);
  const [rowErrors, setRowErrors] = useState<Record<string, CoefficientAdjustmentError>>({});
  const [bulkResult, setBulkResult] = useState<{ updated: number; failed: number } | null>(null);
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
  const channelOptions = useMemo<ChannelFilterOption[]>(() => {
    const map = new Map<string, ChannelFilterOption>();
    for (const row of props.data.items) {
      for (const channel of row.channel_costs) {
        const id = String(channel.channel_id);
        const entry = map.get(id) ?? { id, name: channel.channel_name, models: 0 };
        entry.models += 1;
        map.set(id, entry);
      }
    }
    return [...map.values()].sort((left, right) => left.name.localeCompare(right.name));
  }, [props.data.items]);
  const visible = useMemo(() => {
    const query = search.trim().toLowerCase();
    return props.data.items.filter((row) => {
      if (
        channels.length &&
        !row.channel_costs.some((channel) => channels.includes(String(channel.channel_id)))
      ) {
        return false;
      }
      if (!query) return true;
      return (
        row.origin_model_name.toLowerCase().includes(query) ||
        row.channel_names.some((name) => name.toLowerCase().includes(query))
      );
    });
  }, [channels, props.data.items, search]);
  const pageSize = 20;
  const pageCount = Math.max(1, Math.ceil(visible.length / pageSize));
  const currentPage = Math.min(page, pageCount - 1);
  const pageRows = visible.slice(currentPage * pageSize, (currentPage + 1) * pageSize);
  const selectedRows = props.data.items.filter((row) => selected.includes(row.origin_model_name));
  const allPageSelected = pageRows.length > 0 && pageRows.every((row) => selected.includes(row.origin_model_name));

  function update(model: string, key: "agencyCost" | "defaultSales", value: string) {
    setDraft((current) => ({ ...current, [model]: { ...current[model], [key]: value } }));
    setRowErrors((current) => dropRowError(current, model));
    setBulkResult(null);
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

  function toggleChannel(id: string) {
    setChannels((current) =>
      current.includes(id) ? current.filter((value) => value !== id) : [...current, id],
    );
    setPage(0);
  }

  function toggleModel(model: string) {
    setSelected((current) =>
      current.includes(model) ? current.filter((value) => value !== model) : [...current, model],
    );
    setRowErrors({});
    setBulkResult(null);
  }

  function togglePage() {
    const models = pageRows.map((row) => row.origin_model_name);
    setSelected((current) =>
      allPageSelected
        ? current.filter((model) => !models.includes(model))
        : Array.from(new Set([...current, ...models])),
    );
    setRowErrors({});
    setBulkResult(null);
  }

  function rowCostBPS(row: PlatformPriceRow): number | null {
    const value = draft[row.origin_model_name];
    const drafted = row.channel_costs
      .map((channel) => value?.channelCosts[String(channel.channel_id)]?.trim() ?? "")
      .filter((text) => text !== "")
      .flatMap((text) => {
        try {
          return [parseCoefficient(text)];
        } catch {
          return [];
        }
      });
    if (drafted.length) return Math.max(...drafted);
    return row.platform_cost_bps;
  }

  function adjustmentRows(): CoefficientAdjustmentRow[] {
    return selectedRows.map((row) => ({
      model: row.origin_model_name,
      costBPS: rowCostBPS(row),
      inheritedBPS: adjustTarget === "agencyCost" ? row.agency_cost_bps : row.default_sales_bps,
    }));
  }

  function applyAdjustment(outcome: AdjustmentOutcome) {
    if (!adjustTarget) return;
    const key = adjustTarget;
    setDraft((current) => {
      const next = { ...current };
      for (const model of selected) {
        if (outcome.values[model] == null) continue;
        next[model] = { ...next[model], [key]: outcome.values[model] };
      }
      return next;
    });
    setRowErrors(outcome.errors);
    setBulkResult({ updated: outcome.updated, failed: Object.keys(outcome.errors).length });
    setAdjustTarget(null);
    setSelected([]);
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
      <ChannelFilter
        channels={channelOptions}
        selected={channels}
        onToggle={toggleChannel}
        onClear={() => { setChannels([]); setPage(0); }}
      />
      <div className="pricing-bulk-bar">
        <strong>{t("Batch adjust selected models")}</strong>
        <span>{t("Selected")}: {selected.length}</span>
        <button
          type="button"
          className="secondary button-icon compact-action"
          disabled={!selected.length}
          onClick={() => setAdjustTarget("agencyCost")}
        >
          <ActionIcon name="edit" />
          {t("Increase/decrease downstream channel prices")}
        </button>
        <button
          type="button"
          className="secondary button-icon compact-action"
          disabled={!selected.length}
          onClick={() => setAdjustTarget("defaultSales")}
        >
          <ActionIcon name="edit" />
          {t("Increase/decrease sales prices")}
        </button>
        {bulkResult && (
          <p
            className={bulkResult.failed ? "customer-pricing-bulk-result warning" : "customer-pricing-bulk-result success"}
            role="status"
          >
            {t("Adjusted {{updated}} models. {{failed}} models were not changed.", bulkResult)}
          </p>
        )}
      </div>
      <div className="table-wrap pricing-matrix-wrap">
        <table className="pricing-matrix">
          <thead>
            <tr>
              <th>
                <input
                  type="checkbox"
                  aria-label={t("Select all models on this page")}
                  checked={allPageSelected}
                  onChange={togglePage}
                />
              </th>
              <th>{t("Model name")}</th>
              <th>{t("Channel name")}</th>
              <th>{t("My cost (coefficient)")}</th>
              <th>{t("My downstream channel price (coefficient)")}</th>
              <th>{t("Sales price (coefficient)")}</th>
            </tr>
          </thead>
          <tbody>
            {pageRows.map((row) => (
              <PlatformPricingRow
                key={row.origin_model_name}
                row={row}
                value={draft[row.origin_model_name]}
                selected={selected.includes(row.origin_model_name)}
                toggle={toggleModel}
                update={update}
                updateChannelCost={updateChannelCost}
              />
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
      {props.data.items.length > 0 && visible.length === 0 && (
        <p className="empty">{t("No models match the current filters.")}</p>
      )}
      <div className="pricing-publish-row">
        <Field label={t("Change reason (optional)")}>
          <input value={reason} maxLength={2000} onChange={(event) => setReason(event.target.value)} />
        </Field>
        <button className="button-icon" type="button" disabled={mutation.pending} onClick={() => void publish()}>
          <ActionIcon name="save" />
          {t("Publish platform pricing")}
        </button>
      </div>
      <ErrorNotice error={error} />
      {adjustTarget && (
        <CoefficientAdjustDialog
          title={
            adjustTarget === "agencyCost"
              ? t("Increase/decrease downstream channel prices")
              : t("Increase/decrease sales prices")
          }
          description={
            adjustTarget === "agencyCost"
              ? t("Adjusted downstream channel prices must stay at or above my cost.")
              : t("Adjusted sales prices must stay at or above my cost plus the minimum spread and within the sales cap.")
          }
          rows={adjustmentRows()}
          values={Object.fromEntries(
            selectedRows.map((row) => [
              row.origin_model_name,
              adjustTarget === "agencyCost"
                ? draft[row.origin_model_name]?.agencyCost ?? ""
                : draft[row.origin_model_name]?.defaultSales ?? "",
            ]),
          )}
          options={{ anchor: "cost", capBPS: 100000 }}
          onApply={applyAdjustment}
          onClose={() => setAdjustTarget(null)}
        />
      )}
    </section>
  );
}

function PlatformPricingRow(props: {
  row: PlatformPriceRow;
  value: PlatformDraft[string];
  selected: boolean;
  toggle: (model: string) => void;
  update: (model: string, key: "agencyCost" | "defaultSales", value: string) => void;
  updateChannelCost: (model: string, channelID: number, value: string) => void;
}) {
  const { t } = useTranslation();
  const value = props.value ?? { channelCosts: {}, agencyCost: "", defaultSales: "" };
  return (
    <tr>
      <td>
        <input
          type="checkbox"
          aria-label={`${t("Select model")}: ${props.row.origin_model_name}`}
          checked={props.selected}
          onChange={() => props.toggle(props.row.origin_model_name)}
        />
      </td>
      <td><strong>{props.row.origin_model_name}</strong></td>
      <td>
        <ChannelTags
          names={props.row.channel_costs.map((channel) => ({ id: channel.channel_id, name: channel.channel_name }))}
        />
      </td>
      <td>
        <div className="channel-cost-list">
          {props.row.channel_costs.map((channel) => (
            <input
              key={channel.channel_id}
              aria-label={`${t("My cost (coefficient)")}: ${props.row.origin_model_name} / ${channel.channel_name}`}
              inputMode="decimal"
              placeholder="0.5000"
              value={value.channelCosts[String(channel.channel_id)] ?? ""}
              onChange={(event) => props.updateChannelCost(props.row.origin_model_name, channel.channel_id, event.target.value)}
            />
          ))}
          {props.row.channel_costs.length === 0 && (
            <span className="muted">{t("Channel unavailable")}</span>
          )}
        </div>
      </td>
      <CoefficientInput label={t("My downstream channel price (coefficient)")} model={props.row.origin_model_name} value={value.agencyCost} placeholder="0.5500" onChange={(value) => props.update(props.row.origin_model_name, "agencyCost", value)} />
      <CoefficientInput label={t("Sales price (coefficient)")} model={props.row.origin_model_name} value={value.defaultSales} placeholder="0.6000" onChange={(value) => props.update(props.row.origin_model_name, "defaultSales", value)} />
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

function dropRowError(
  current: Record<string, CoefficientAdjustmentError>,
  model: string,
): Record<string, CoefficientAdjustmentError> {
  if (!(model in current)) return current;
  const next = { ...current };
  delete next[model];
  return next;
}
