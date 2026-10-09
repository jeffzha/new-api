import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { ActionIcon } from "../../components/Heading";
import { ErrorNotice, Field, Loading, Pager } from "../../components/ui";
import { useQuery } from "../../lib/client";
import { useMutation } from "../../lib/mutations";
import { CoefficientAdjustDialog, type AdjustmentOutcome } from "./AdjustDialog";
import { ChannelTags, type ChannelFilterOption } from "./ChannelFilter";
import { ChannelFilterMenu, VendorTabs } from "./VendorFilter";
import { activeVendorKey, buildVendorOptions, matchesPricingSearch, vendorKeyOf } from "./vendorOptions";
import {
  formatCoefficient,
  parseCoefficient,
  type CoefficientAdjustmentError,
  type CoefficientAdjustmentRow,
} from "./policy";
import type { ModelSales } from "./types";

type AdjustTarget = "childCost" | "sales";

export function AgencySalesEditor(props: { root: boolean; agencyId: string | null }) {
  const path = props.root ? `/root/agencies/${props.agencyId}/pricing` : "/pricing";
  const pricing = useQuery<ModelSales>(path + "/model-sales");
  if (pricing.loading) return <Loading />;
  if (!pricing.data) return <ErrorNotice error={pricing.error} />;
  return (
    <AgencySalesForm
      key={`${pricing.data.agency_id}:${pricing.data.revision}:${pricing.data.platform_revision}`}
      root={props.root}
      path={path}
      data={pricing.data}
      reload={pricing.reload}
    />
  );
}

function AgencySalesForm(props: {
  root: boolean;
  path: string;
  data: ModelSales;
  reload: () => void;
}) {
  const { t } = useTranslation();
  const mutation = useMutation();
  const [reason, setReason] = useState("");
  const [minSpread, setMinSpread] = useState(() => formatCoefficient(props.data.min_spread_bps));
  const [error, setError] = useState<unknown>(null);
  const [search, setSearch] = useState("");
  const [page, setPage] = useState(0);
  const [channels, setChannels] = useState<string[]>([]);
  const [vendorFilter, setVendorFilter] = useState("all");
  const [selected, setSelected] = useState<string[]>([]);
  const [adjustTarget, setAdjustTarget] = useState<AdjustTarget | null>(null);
  const [rowErrors, setRowErrors] = useState<Record<string, CoefficientAdjustmentError>>({});
  const [errorTarget, setErrorTarget] = useState<AdjustTarget | null>(null);
  const [bulkResult, setBulkResult] = useState<{ updated: number; failed: number } | null>(null);
  const [defaultSales, setDefaultSales] = useState(() => formatCoefficient(props.data.default_sales_bps));
  const [defaultChildCost, setDefaultChildCost] = useState(() => props.data.default_child_cost_bps ? formatCoefficient(props.data.default_child_cost_bps) : "");
  const [childCosts, setChildCosts] = useState<Record<string, string>>(() =>
    Object.fromEntries(props.data.items.map((row) => [
      row.origin_model_name,
      row.override_child_cost_bps == null ? "" : formatCoefficient(row.override_child_cost_bps),
    ])),
  );
  const [sales, setSales] = useState<Record<string, string>>(() =>
    Object.fromEntries(
      props.data.items.map((row) => [
        row.origin_model_name,
        row.override_sales_bps == null ? "" : formatCoefficient(row.override_sales_bps),
      ]),
    ),
  );
  const channelOptions = useMemo<ChannelFilterOption[]>(() => {
    const map = new Map<string, ChannelFilterOption>();
    for (const row of props.data.items) {
      for (const channel of row.channels ?? []) {
        const id = String(channel.channel_id);
        const entry = map.get(id) ?? { id, name: channel.channel_name, models: 0 };
        entry.models += 1;
        map.set(id, entry);
      }
    }
    return [...map.values()].sort((left, right) => left.name.localeCompare(right.name));
  }, [props.data.items]);
  const channelRows = useMemo(() => {
    if (!channels.length) return props.data.items;
    return props.data.items.filter((row) =>
      (row.channels ?? []).some((channel) => channels.includes(String(channel.channel_id))),
    );
  }, [channels, props.data.items]);
  const vendorOptions = useMemo(
    () => buildVendorOptions(channelRows, t("Other providers")),
    [channelRows, t],
  );
  const activeVendor = activeVendorKey(vendorFilter, vendorOptions);
  const visible = useMemo(() => {
    return channelRows.filter(
      (row) =>
        (activeVendor === "all" || vendorKeyOf(row) === activeVendor) &&
        matchesPricingSearch(search, {
          origin_model_name: row.origin_model_name,
          vendor_name: row.vendor_name,
          channelNames: (row.channels ?? []).map((channel) => channel.channel_name),
        }),
    );
  }, [activeVendor, channelRows, search]);
  const pageSize = 20;
  const pageCount = Math.max(1, Math.ceil(visible.length / pageSize));
  const currentPage = Math.min(page, pageCount - 1);
  const pageRows = visible.slice(currentPage * pageSize, (currentPage + 1) * pageSize);
  const selectedRows = props.data.items.filter((row) => selected.includes(row.origin_model_name));
  const allPageSelected = pageRows.length > 0 && pageRows.every((row) => selected.includes(row.origin_model_name));
  const capBPS = props.data.sales_cap_bps ?? 100000;

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

  function adjustmentRows(): CoefficientAdjustmentRow[] {
    return selectedRows.map((row) => ({
      model: row.origin_model_name,
      costBPS: row.agency_cost_bps,
      inheritedBPS: adjustTarget === "childCost" ? row.child_cost_bps : row.sales_bps,
    }));
  }

  function applyAdjustment(outcome: AdjustmentOutcome) {
    if (!adjustTarget) return;
    if (adjustTarget === "childCost") {
      setChildCosts((current) => ({ ...current, ...pickAdjusted(outcome, selected) }));
    } else {
      setSales((current) => ({ ...current, ...pickAdjusted(outcome, selected) }));
    }
    setRowErrors(outcome.errors);
    setErrorTarget(adjustTarget);
    setBulkResult({ updated: outcome.updated, failed: Object.keys(outcome.errors).length });
    setAdjustTarget(null);
    setSelected([]);
  }

  function updateChildCost(model: string, value: string) {
    setChildCosts((current) => ({ ...current, [model]: value }));
    setRowErrors((current) => dropRowError(current, model));
    setErrorTarget(null);
    setBulkResult(null);
  }

  function updateSales(model: string, value: string) {
    setSales((current) => ({ ...current, [model]: value }));
    setRowErrors((current) => dropRowError(current, model));
    setErrorTarget(null);
    setBulkResult(null);
  }

  function rowErrorMessage(code: CoefficientAdjustmentError) {
    if (code === "missing_cost") return t("My cost is missing for this model.");
    if (code === "below_cost") return t("This adjustment would be below the agency cost.");
    if (code === "below_spread")
      return t(
        "This adjustment is above cost but does not meet the minimum spread. Use the exact cost price or meet the full minimum spread.",
      );
    if (code === "above_cap") return t("This adjustment would exceed the sales cap.");
    return t("The current coefficient is invalid. Enter a valid coefficient before adjusting it.");
  }

  async function publish() {
    setError(null);
    try {
      await mutation.mutate(
        props.root ? props.path + "/sales/publish" : "/pricing/sales/publish",
        {
          expected_revision: props.data.revision,
          ...(props.root ? { min_spread_bps: parseCoefficient(minSpread) } : {}),
          default_sales_bps: parseCoefficient(defaultSales),
          default_child_cost_bps: defaultChildCost.trim() === "" ? 0 : parseCoefficient(defaultChildCost),
          model_sales_overrides: props.data.items.map((row) => ({
            origin_model_name: row.origin_model_name,
            sales_bps:
              sales[row.origin_model_name] === ""
                ? null
                : parseCoefficient(sales[row.origin_model_name]),
          })),
          model_child_cost_overrides: props.data.items.map((row) => ({
            origin_model_name: row.origin_model_name,
            child_cost_bps: childCosts[row.origin_model_name] === "" ? null : parseCoefficient(childCosts[row.origin_model_name]),
          })),
          reason,
        },
        { action: "pricing.sales.publish", objectId: `agency:${props.data.agency_id}` },
      );
      props.reload();
    } catch (cause) {
      setError(cause);
    }
  }

  return (
    <section className="pricing-matrix-card agency-sales-card">
      <div className="toolbar pricing-toolbar">
        <div>
          <h3>{props.data.agency_name}</h3>
          <p className="muted">
            {t("Agency cost is inherited and read-only. Set your customer sales coefficient and the cost inherited by your direct child agencies.")}
          </p>
        </div>
        <button className="secondary button-icon" type="button" onClick={props.reload}>
          <ActionIcon name="refresh" />
          {t("Refresh")}
        </button>
      </div>
      <div className="pricing-defaults-grid">
        <Field label={t("Minimum spread")}>
          <input inputMode="decimal" value={minSpread} readOnly={!props.root} onChange={(event) => setMinSpread(event.target.value)} />
        </Field>
        <Field label={t("Default sales price (coefficient)")}>
          <input inputMode="decimal" value={defaultSales} onChange={(event) => setDefaultSales(event.target.value)} />
          <small>{t("Used when a model or customer has no more specific sales override.")}</small>
        </Field>
        <Field label={t("Default downstream channel price (coefficient)")}>
          <input inputMode="decimal" value={defaultChildCost} onChange={(event) => setDefaultChildCost(event.target.value)} />
          <small>{t("Used by direct child agencies when a model has no specific cost override.")}</small>
        </Field>
      </div>
      <div className="pricing-search">
        <label className="search-field">
          <ActionIcon name="search" />
          <input
            aria-label={t("Search models, providers, or channels")}
            placeholder={t("Search models, providers, or channels")}
            value={search}
            onChange={(event) => { setSearch(event.target.value); setPage(0); }}
          />
        </label>
        <ChannelFilterMenu
          channels={channelOptions}
          selected={channels}
          onToggle={toggleChannel}
          onClear={() => { setChannels([]); setPage(0); }}
        />
        <span className="pricing-live-badge">{t("Models")} · {visible.length}</span>
      </div>
      <VendorTabs
        vendors={vendorOptions}
        selected={activeVendor}
        total={channelRows.length}
        onSelect={(key) => { setVendorFilter(key); setPage(0); }}
      />
      <div className="pricing-bulk-bar">
        <strong>{t("Batch adjust selected models")}</strong>
        <span>{t("Selected")}: {selected.length}</span>
        <button
          type="button"
          className="secondary button-icon compact-action"
          disabled={!selected.length}
          onClick={() => setAdjustTarget("childCost")}
        >
          <ActionIcon name="edit" />
          {t("Increase/decrease downstream channel prices")}
        </button>
        <button
          type="button"
          className="secondary button-icon compact-action"
          disabled={!selected.length}
          onClick={() => setAdjustTarget("sales")}
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
              <th>{t("Channels")}</th>
              <th>{t("My cost (coefficient)")}</th>
              <th>{t("My downstream channel price (coefficient)")}</th>
              <th>{t("Sales price (coefficient)")}</th>
            </tr>
          </thead>
          <tbody>
            {pageRows.map((row) => {
              const rowError = rowErrors[row.origin_model_name];
              return (
                <tr key={row.origin_model_name} className={rowError ? "customer-pricing-row-error" : undefined}>
                  <td>
                    <input
                      type="checkbox"
                      aria-label={`${t("Select model")}: ${row.origin_model_name}`}
                      checked={selected.includes(row.origin_model_name)}
                      onChange={() => toggleModel(row.origin_model_name)}
                    />
                  </td>
                  <td><strong>{row.origin_model_name}</strong></td>
                  <td>
                    <ChannelTags
                      names={(row.channels ?? []).map((channel) => ({ id: channel.channel_id, name: channel.channel_name }))}
                    />
                  </td>
                  <td><span className="coefficient-readonly">{formatCoefficient(row.agency_cost_bps)}</span></td>
                  <td>
                    <input
                      aria-label={`${t("My downstream channel price (coefficient)")}: ${row.origin_model_name}`}
                      inputMode="decimal"
                      value={childCosts[row.origin_model_name]}
                      placeholder={row.child_cost_bps ? formatCoefficient(row.child_cost_bps) : t("Not configured")}
                      onChange={(event) => updateChildCost(row.origin_model_name, event.target.value)}
                    />
                    <small>
                      {childCosts[row.origin_model_name] === ""
                        ? row.child_cost_bps
                          ? t("Using inherited child cost {{value}}", { value: formatCoefficient(row.child_cost_bps) })
                          : t("Not configured")
                        : t("Agency override")}
                    </small>
                    {rowError && errorTarget === "childCost" && (
                      <small className="customer-pricing-inline-error">{rowErrorMessage(rowError)}</small>
                    )}
                  </td>
                  <td>
                    <div className="sales-coefficient-field">
                      <input
                        aria-label={`${t("Sales price (coefficient)")}: ${row.origin_model_name}`}
                        inputMode="decimal"
                        value={sales[row.origin_model_name]}
                        placeholder={formatCoefficient(row.platform_default_sales_bps)}
                        onChange={(event) => updateSales(row.origin_model_name, event.target.value)}
                      />
                      <small>
                        {sales[row.origin_model_name] === ""
                          ? t("Using platform default {{value}}", {
                              value: formatCoefficient(row.platform_default_sales_bps),
                            })
                          : t("Agency override")}
                      </small>
                      {rowError && errorTarget === "sales" && (
                        <small className="customer-pricing-inline-error">{rowErrorMessage(rowError)}</small>
                      )}
                    </div>
                  </td>
                </tr>
              );
            })}
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
        <p className="empty">{t("Configure platform model coefficients first.")}</p>
      )}
      {props.data.items.length > 0 && visible.length === 0 && (
        <p className="empty">{t("No models match the current filters.")}</p>
      )}
      <div className="pricing-publish-row">
        <Field label={t("Change reason (optional)")}>
          <input value={reason} maxLength={2000} onChange={(event) => setReason(event.target.value)} />
        </Field>
        <button
          className="button-icon"
          type="button"
          disabled={mutation.pending || props.data.items.length === 0}
          onClick={() => void publish()}
        >
          <ActionIcon name="save" />
          {t("Publish sales coefficients")}
        </button>
      </div>
      <ErrorNotice error={error} />
      {adjustTarget && (
        <CoefficientAdjustDialog
          title={
            adjustTarget === "childCost"
              ? t("Increase/decrease downstream channel prices")
              : t("Increase/decrease sales prices")
          }
          description={
            adjustTarget === "childCost"
              ? t("Adjusted downstream channel prices must stay at or above my cost.")
              : t("Adjusted sales prices must stay at or above my cost plus the minimum spread and within the sales cap.")
          }
          rows={adjustmentRows()}
          values={Object.fromEntries(
            selectedRows.map((row) => [
              row.origin_model_name,
              adjustTarget === "childCost"
                ? childCosts[row.origin_model_name] ?? ""
                : sales[row.origin_model_name] ?? "",
            ]),
          )}
          options={{
            anchor: "cost",
            minSpreadBPS: adjustTarget === "sales" ? props.data.min_spread_bps : 0,
            capBPS: adjustTarget === "sales" ? capBPS : 100000,
          }}
          onApply={applyAdjustment}
          onClose={() => setAdjustTarget(null)}
        />
      )}
    </section>
  );
}

function pickAdjusted(outcome: AdjustmentOutcome, selected: string[]) {
  const picked: Record<string, string> = {};
  for (const model of selected) {
    const value = outcome.values[model];
    if (value != null) picked[model] = value;
  }
  return picked;
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
