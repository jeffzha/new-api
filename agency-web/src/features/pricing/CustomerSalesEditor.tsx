import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { ActionIcon } from "../../components/Heading";
import { PageHeader } from "../../components/PageHeader";
import { ErrorNotice, Field, Loading, Pager } from "../../components/ui";
import { useMutation } from "../../lib/mutations";
import { useQuery } from "../../lib/query";
import {
  adjustCustomerSalesValues,
  type CustomerSalesAdjustmentError,
  formatCoefficient,
  parseCoefficient,
  parseSignedCoefficient,
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
type ModelsResponse = { items: string[] };
type CustomerSalesRow = {
  model: string;
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
  const rows = models.data.items.map((model): CustomerSalesRow => {
    const item = overrides.get(model);
    return {
      model,
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
  const [adjustment, setAdjustment] = useState("");
  const [globalValue, setGlobalValue] = useState(props.globalSalesBPS == null ? "" : formatCoefficient(props.globalSalesBPS));
  const [values, setValues] = useState<Record<string, string>>(() => Object.fromEntries(props.rows.map((row) => [row.model, row.overrideSalesBPS == null ? "" : formatCoefficient(row.overrideSalesBPS)])));
  const [reason, setReason] = useState("");
  const [error, setError] = useState<unknown>(null);
  const [rowErrors, setRowErrors] = useState<Record<string, CustomerSalesAdjustmentError>>({});
  const [bulkResult, setBulkResult] = useState<{ updated: number; failed: number } | null>(null);
  const visibleRows = useMemo(() => { const query = search.trim().toLowerCase(); return query ? props.rows.filter((row) => row.model.toLowerCase().includes(query)) : props.rows; }, [props.rows, search]);
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
  function applyAdjustment() {
    setError(null);
    let deltaBPS: number;
    try {
      deltaBPS = parseSignedCoefficient(adjustment.trim());
    } catch {
      deltaBPS = 0;
    }
    if (!selectedRows.length || deltaBPS === 0) {
      setError(new Error(t("Enter a valid adjustment, such as 0.1000 or -0.1000.")));
      return;
    }
    const result = adjustCustomerSalesValues(
      props.rows,
      values,
      selected,
      deltaBPS,
      props.minSpreadBPS,
      props.salesCapBPS,
    );
    setValues(result.values);
    setRowErrors(result.errors);
    setBulkResult({ updated: result.updated, failed: Object.keys(result.errors).length });
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
  function rowErrorMessage(code: CustomerSalesAdjustmentError) {
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
    <div className="pricing-search customer-pricing-search"><Field label={t("Search models")}><input value={search} onChange={(event) => { setSearch(event.target.value); setPage(0); }} /></Field><span className="pricing-live-badge">{t("Models")} · {visibleRows.length}</span></div>
    <div className="customer-pricing-bulk"><strong>{t("Batch adjust selected models")}</strong><span>{t("Selected")}: {selected.length}</span><input aria-label={t("Adjustment amount")} inputMode="decimal" placeholder="0.1000 / -0.1000" value={adjustment} onChange={(event) => setAdjustment(event.target.value)} /><button type="button" className="secondary" disabled={!selected.length} onClick={applyAdjustment}>{t("Increase/decrease sales prices")}</button><small>{t("Enter a positive value to increase sales prices or a negative value to decrease them. Cost, minimum spread and sales cap still apply.")}</small>{bulkResult && <p className={bulkResult.failed ? "customer-pricing-bulk-result warning" : "customer-pricing-bulk-result success"} role="status">{t("Adjusted {{updated}} models. {{failed}} models were not changed.", bulkResult)}</p>}</div>
    <div className="table-wrap pricing-matrix-wrap customer-pricing-matrix-wrap customer-pricing-page-matrix"><table className="pricing-matrix customer-pricing-matrix"><thead><tr><th><input type="checkbox" aria-label={t("Select visible models")} checked={allVisibleSelected} onChange={toggleVisible} /></th><th>{t("Model name")}</th><th>{t("My cost (coefficient)")}</th><th>{t("Sales price (coefficient)")}</th></tr></thead><tbody>{pageRows.map((row) => { const rowError = rowErrors[row.model]; return <tr key={row.model} className={rowError ? "customer-pricing-row-error" : undefined}><td><input type="checkbox" aria-label={t("Select model") + ": " + row.model} checked={selected.includes(row.model)} onChange={() => toggle(row.model)} /></td><td><strong>{row.model}</strong></td><td><span className="coefficient-readonly">{formatCoefficient(row.agencyCostBPS)}</span></td><td><input aria-label={t("Sales price (coefficient)") + ": " + row.model} aria-invalid={rowError ? "true" : undefined} inputMode="decimal" placeholder={formatCoefficient(row.inheritedSalesBPS)} value={values[row.model] ?? ""} onChange={(event) => updateValue(row.model, event.target.value)} />{rowError ? <small className="customer-pricing-inline-error">{rowErrorMessage(rowError)}</small> : <small>{values[row.model] ? t("Customer override") : t("Using inherited sales price {{value}}", { value: formatCoefficient(row.inheritedSalesBPS) })}</small>}</td></tr>; })}</tbody></table></div>
    {visibleRows.length > pageSize && <Pager nextCursor={currentPage < pageCount - 1 ? String(currentPage + 1) : undefined} hasPrevious={currentPage > 0} onNext={() => setPage((value) => Math.min(value + 1, pageCount - 1))} onReset={() => setPage(0)} />}
    {!visibleRows.length && <p className="empty">{t("No models are available for customer pricing.")}</p>}
    <div className="pricing-publish-row"><Field label={t("Change reason (optional)")}><input value={reason} maxLength={2000} onChange={(event) => setReason(event.target.value)} /></Field><button type="button" disabled={mutation.pending} onClick={() => void save()}>{t("Save customer pricing")}</button></div><ErrorNotice error={error} /><div className="actions customer-pricing-page-actions"><button type="button" className="secondary" disabled={mutation.pending} onClick={props.onBack}>{t("Cancel")}</button></div>
  </section>;
}

function BackButton(props: { onBack: () => void; disabled?: boolean }) {
  const { t } = useTranslation();
  return <button className="secondary button-icon compact-action" type="button" disabled={props.disabled} onClick={props.onBack}><ActionIcon name="arrow-left" />{t("Back to customer management")}</button>;
}
