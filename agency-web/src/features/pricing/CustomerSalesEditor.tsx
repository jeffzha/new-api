import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { ActionIcon } from "../../components/Heading";
import { PageHeader } from "../../components/PageHeader";
import { ErrorNotice, Field, Loading, Pager } from "../../components/ui";
import { useMutation } from "../../lib/mutations";
import { useQuery } from "../../lib/query";
import { CoefficientAdjustDialog, type AdjustmentOutcome } from "./AdjustDialog";
import { ChannelPricingRows, type ChannelFilterOption } from "./ChannelFilter";
import { ChannelFilterMenu, VendorMark, VendorTabs } from "./VendorFilter";
import { activeVendorKey, buildVendorOptions, matchesPricingSearch, vendorKeyOf } from "./vendorOptions";
import {
  type CoefficientAdjustmentError,
  formatCoefficient,
  parseCoefficient,
} from "./policy";

type Override = {
  origin_model_name: string;
  model_key: string;
  agency_cost_bps?: number | null;
  inherited_sales_bps?: number | null;
  override_sales_bps?: number | null;
  is_global?: boolean;
};
type SalesResponse = {
  default_sales_bps: number;
  default_agency_cost_bps?: number;
  min_spread_bps?: number;
  sales_cap_bps?: number;
  items: Override[];
};
type ModelCatalogItem = {
  model: string;
  vendor_name?: string;
  vendor_icon?: string;
  channels?: { channel_id: number; channel_name: string }[];
};
type ModelsResponse = { items: string[]; catalog?: ModelCatalogItem[] };
type CustomerSalesRow = {
  model: string;
  vendorName?: string;
  vendorIcon?: string;
  channels: { channel_id: number; channel_name: string }[];
  agencyCostBPS: number;
  inheritedSalesBPS: number;
  overrideSalesBPS: number | null;
};

export function CustomerSalesEditor(props: { userId: string | number; username: string; onBack: () => void }) {
  const { t } = useTranslation();
  const pricing = useQuery<SalesResponse>("/customers/" + props.userId + "/pricing");
  const models = useQuery<ModelsResponse>("/models?limit=1000");
  if (pricing.loading || models.loading) return <section><PageHeader icon="pricing" title={t("Customer sales pricing")} actions={<BackButton onBack={props.onBack} />} /><Loading /></section>;
  if (!pricing.data || !models.data) return <section><PageHeader icon="pricing" title={t("Customer sales pricing")} actions={<BackButton onBack={props.onBack} />} /><ErrorNotice error={pricing.error || models.error} /></section>;
  const data = pricing.data;
  const overrides = new Map(data.items.filter((item) => item.origin_model_name).map((item) => [item.origin_model_name, item]));
  const catalog = new Map((models.data.catalog ?? []).map((item) => [item.model, item]));
  const rows = models.data.items.map((model): CustomerSalesRow => {
    const item = overrides.get(model);
    const metadata = catalog.get(model);
    return {
      model,
      vendorName: metadata?.vendor_name,
      vendorIcon: metadata?.vendor_icon,
      channels: metadata?.channels ?? [],
      agencyCostBPS: item?.agency_cost_bps ?? data.default_agency_cost_bps ?? 0,
      inheritedSalesBPS: item?.inherited_sales_bps ?? data.default_sales_bps,
      overrideSalesBPS: item?.override_sales_bps ?? null,
    };
  });
  const global = data.items.find((item) => item.is_global);
  return <CustomerSalesMatrix key={String(props.userId) + ":" + rows.map((row) => row.model + ":" + String(row.overrideSalesBPS ?? "")).join("|")} userId={props.userId} username={props.username} rows={rows} globalSalesBPS={global?.override_sales_bps ?? null} agencyDefaultSalesBPS={data.default_sales_bps} minSpreadBPS={data.min_spread_bps ?? 0} salesCapBPS={data.sales_cap_bps ?? 100000} onBack={props.onBack} />;
}

function CustomerSalesMatrix(props: { userId: string | number; username: string; rows: CustomerSalesRow[]; globalSalesBPS: number | null; agencyDefaultSalesBPS: number; minSpreadBPS: number; salesCapBPS: number; onBack: () => void }) {
  const { t } = useTranslation();
  const mutation = useMutation();
  const [search, setSearch] = useState("");
  const [page, setPage] = useState(0);
  const [selected, setSelected] = useState<string[]>([]);
  const [adjustTarget, setAdjustTarget] = useState(false);
  const [globalValue, setGlobalValue] = useState(props.globalSalesBPS == null ? "" : formatCoefficient(props.globalSalesBPS));
  const [values, setValues] = useState<Record<string, string>>(() => Object.fromEntries(props.rows.map((row) => [row.model, row.overrideSalesBPS == null ? "" : formatCoefficient(row.overrideSalesBPS)])));
  const [reason, setReason] = useState("");
  const [error, setError] = useState<unknown>(null);
  const [rowErrors, setRowErrors] = useState<Record<string, CoefficientAdjustmentError>>({});
  const [bulkResult, setBulkResult] = useState<{ updated: number; failed: number } | null>(null);
  const [channels, setChannels] = useState<string[]>([]);
  const [vendorFilter, setVendorFilter] = useState("all");
  const channelRows = useMemo(() => channels.length ? props.rows.filter((row) => row.channels.some((channel) => channels.includes(String(channel.channel_id)))) : props.rows, [channels, props.rows]);
  const channelOptions = useMemo<ChannelFilterOption[]>(() => {
    const map = new Map<string, ChannelFilterOption>();
    for (const row of props.rows) for (const channel of row.channels) {
      const id = String(channel.channel_id);
      const current = map.get(id) ?? { id, name: channel.channel_name, models: 0 };
      current.models += 1;
      map.set(id, current);
    }
    return [...map.values()].sort((left, right) => left.name.localeCompare(right.name));
  }, [props.rows]);
  const vendorOptions = useMemo(() => buildVendorOptions(channelRows.map((row) => ({ vendor_name: row.vendorName, vendor_icon: row.vendorIcon })), t("Other providers")), [channelRows, t]);
  const activeVendor = activeVendorKey(vendorFilter, vendorOptions);
  const visibleRows = useMemo(() => channelRows.filter((row) => (activeVendor === "all" || vendorKeyOf({ vendor_name: row.vendorName }) === activeVendor) && matchesPricingSearch(search, { origin_model_name: row.model, vendor_name: row.vendorName, channelNames: row.channels.map((channel) => channel.channel_name) })), [activeVendor, channelRows, search]);
  const pageSize = 20;
  const pageCount = Math.max(1, Math.ceil(visibleRows.length / pageSize));
  const currentPage = Math.min(page, pageCount - 1);
  const pageRows = visibleRows.slice(currentPage * pageSize, (currentPage + 1) * pageSize);
  const selectedRows = props.rows.filter((row) => selected.includes(row.model));

  function toggle(model: string) {
    setSelected((current) => current.includes(model) ? current.filter((value) => value !== model) : [...current, model]);
  }
  function toggleVisible() {
    const pageModels = pageRows.map((row) => row.model);
    const allSelected = pageModels.every((model) => selected.includes(model));
    setSelected((current) => allSelected ? current.filter((model) => !pageModels.includes(model)) : Array.from(new Set([...current, ...pageModels])));
  }
  function applyAdjustment(result: AdjustmentOutcome) {
    setValues(result.values);
    setRowErrors(result.errors);
    setBulkResult({ updated: result.updated, failed: Object.keys(result.errors).length });
    setSelected([]);
    setAdjustTarget(false);
  }
  function fillCostPrices() {
    setError(null);
    setValues(Object.fromEntries(props.rows.map((row) => [row.model, formatCoefficient(row.agencyCostBPS)])));
    setRowErrors({});
    setBulkResult(null);
  }
  function updateValue(model: string, value: string) {
    setValues((current) => ({ ...current, [model]: value }));
    setRowErrors((current) => {
      if (!(model in current)) return current;
      const next = { ...current };
      delete next[model];
      return next;
    });
    setBulkResult(null);
  }
  function rowErrorMessage(code: CoefficientAdjustmentError) {
    if (code === "below_cost") return t("This adjustment would be below the agency cost.");
    if (code === "below_spread") return t("This adjustment is above cost but does not meet the minimum spread. Use the exact cost price or meet the full minimum spread.");
    if (code === "above_cap") return t("This adjustment would exceed the sales cap.");
    return t("The current sales price is invalid. Enter a valid coefficient before adjusting it.");
  }
  async function save() {
    setError(null);
    try {
      await mutation.mutate("/customers/" + props.userId + "/pricing/batch", { global_sales_bps: globalValue.trim() ? parseCoefficient(globalValue) : null, models: props.rows.map((row) => ({ model_name: row.model, sales_bps: values[row.model]?.trim() ? parseCoefficient(values[row.model]) : null })), reason: reason.trim() }, { method: "PUT", action: "pricing.customer_sales.batch_publish", objectId: "user:" + props.userId, title: t("Save customer pricing") });
      props.onBack();
    } catch (cause) { setError(cause); }
  }
  const allVisibleSelected = pageRows.length > 0 && pageRows.every((row) => selected.includes(row.model));
  return <section className="customer-pricing-page">
    <PageHeader icon="pricing" title={t("Customer sales pricing") + " · " + props.username} description={t("Set prices for this customer only. A blank model value inherits its current sales price.")} actions={<BackButton onBack={props.onBack} disabled={mutation.pending} />} />
    <div className="customer-pricing-summary"><Field label={t("Universal sales price (coefficient)")} hint={t("Leave blank to inherit each model's current sales price.")}><input inputMode="decimal" placeholder={formatCoefficient(props.agencyDefaultSalesBPS)} value={globalValue} onChange={(event) => setGlobalValue(event.target.value)} /></Field><div className="customer-pricing-summary-actions"><button type="button" className="secondary button-icon" onClick={fillCostPrices}><ActionIcon name="check" />{t("Set all models to cost")}</button><small>{t("Use each model's agency cost as this customer's sales price. Review and save to apply.")}</small></div></div>
    <div className="pricing-search customer-pricing-search"><label className="search-field"><ActionIcon name="search" /><input aria-label={t("Search models, providers, or channels")} placeholder={t("Search models, providers, or channels")} value={search} onChange={(event) => { setSearch(event.target.value); setPage(0); }} /></label><ChannelFilterMenu channels={channelOptions} selected={channels} onToggle={(id: string) => { setChannels((current) => current.includes(id) ? current.filter((value) => value !== id) : [...current, id]); setPage(0); }} onClear={() => { setChannels([]); setPage(0); }} /><span className="pricing-live-badge">{t("Models")} · {visibleRows.length}</span></div>
    <VendorTabs vendors={vendorOptions} selected={activeVendor} total={channelRows.length} onSelect={(key) => { setVendorFilter(key); setPage(0); }} />
    <div className="customer-pricing-bulk pricing-bulk-bar"><strong>{t("Batch adjust selected models")}</strong><span>{t("Selected")}: {selected.length}</span><button type="button" className="secondary button-icon compact-action" disabled={!selected.length} onClick={() => setAdjustTarget(true)}><ActionIcon name="edit" />{t("Increase/decrease sales prices")}</button>{bulkResult && <p className={bulkResult.failed ? "customer-pricing-bulk-result warning" : "customer-pricing-bulk-result success"} role="status">{t("Adjusted {{updated}} models. {{failed}} models were not changed.", bulkResult)}</p>}</div>
    <div className="table-wrap pricing-matrix-wrap customer-pricing-matrix-wrap customer-pricing-page-matrix">
      <table className="pricing-matrix customer-pricing-matrix">
        <thead><tr><th><input type="checkbox" aria-label={t("Select visible models")} checked={allVisibleSelected} onChange={toggleVisible} /></th><th>{t("Model name")}</th><th>{t("Channels")}</th><th>{t("My cost (coefficient)")}</th><th>{t("Sales price (coefficient)")}</th></tr></thead>
        <tbody>{pageRows.map((row) => {
          const rowError = rowErrors[row.model];
          const cost = <span className="coefficient-readonly">{formatCoefficient(row.agencyCostBPS)}</span>;
          return <ChannelPricingRows key={row.model} className={rowError ? "customer-pricing-row-error" : undefined}
            leading={[
              <input key="selection" type="checkbox" aria-label={t("Select model") + ": " + row.model} checked={selected.includes(row.model)} onChange={() => toggle(row.model)} />,
              <div key="model" className="model-name-cell"><VendorMark icon={row.vendorIcon} name={row.vendorName?.trim() || t("Other providers")} /><strong>{row.model}</strong></div>,
            ]}
            channels={row.channels.map((channel) => ({ id: channel.channel_id, name: channel.channel_name, cost }))}
            emptyCost={cost}
            trailing={[<div key="sales" className="sales-coefficient-field"><input aria-label={t("Sales price (coefficient)") + ": " + row.model} aria-invalid={rowError ? "true" : undefined} inputMode="decimal" placeholder={formatCoefficient(row.inheritedSalesBPS)} value={values[row.model] ?? ""} onChange={(event) => updateValue(row.model, event.target.value)} />{rowError ? <small className="customer-pricing-inline-error">{rowErrorMessage(rowError)}</small> : <small>{values[row.model] ? t("Customer override") : t("Using inherited sales price {{value}}", { value: formatCoefficient(row.inheritedSalesBPS) })}</small>}</div>]} />;
        })}</tbody>
      </table>
    </div>
    {visibleRows.length > pageSize && <Pager nextCursor={currentPage < pageCount - 1 ? String(currentPage + 1) : undefined} hasPrevious={currentPage > 0} onNext={() => setPage((value) => Math.min(value + 1, pageCount - 1))} onReset={() => setPage(0)} />}
    {!visibleRows.length && <p className="empty">{t("No models are available for customer pricing.")}</p>}
    <div className="pricing-publish-row"><Field label={t("Change reason (optional)")}><input value={reason} maxLength={2000} onChange={(event) => setReason(event.target.value)} /></Field><button type="button" disabled={mutation.pending} onClick={() => void save()}>{t("Save customer pricing")}</button></div><ErrorNotice error={error} /><div className="actions customer-pricing-page-actions"><button type="button" className="secondary" disabled={mutation.pending} onClick={props.onBack}>{t("Cancel")}</button></div>
    {adjustTarget && <CoefficientAdjustDialog title={t("Increase/decrease sales prices")} description={t("Adjusted sales prices must stay at or above my cost plus the minimum spread and within the sales cap.")} rows={selectedRows.map((row) => ({ model: row.model, costBPS: row.agencyCostBPS, inheritedBPS: row.inheritedSalesBPS }))} values={Object.fromEntries(selectedRows.map((row) => [row.model, values[row.model] ?? ""]))} options={{ anchor: "cost", minSpreadBPS: props.minSpreadBPS, capBPS: props.salesCapBPS }} onApply={applyAdjustment} onClose={() => setAdjustTarget(false)} />}
  </section>;
}

function BackButton(props: { onBack: () => void; disabled?: boolean }) {
  const { t } = useTranslation();
  return <button className="secondary button-icon compact-action" type="button" disabled={props.disabled} onClick={props.onBack}><ActionIcon name="arrow-left" />{t("Back to customer management")}</button>;
}
