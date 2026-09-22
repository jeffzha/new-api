import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Dialog, ErrorNotice, Field, Loading, Pager } from "../../components/ui";
import { useMutation } from "../../lib/mutations";
import { useQuery } from "../../lib/query";
import { formatCoefficient, parseCoefficient } from "./policy";

type Override = {
  origin_model_name: string;
  model_key: string;
  inherited_sales_bps?: number | null;
  override_sales_bps?: number | null;
  is_global?: boolean;
};
type SalesResponse = { default_sales_bps: number; items: Override[] };
type ModelsResponse = { items: string[] };
type CustomerSalesRow = { model: string; inheritedSalesBPS: number; overrideSalesBPS: number | null };

// This mirrors the agency price matrix so an operator can review and update a
// customer's model prices in one place. An empty value removes only that
// customer's override and resumes the displayed inherited agency coefficient.
export function CustomerSalesEditor(props: { userId: string | number; username: string; onClose: () => void }) {
  const { t } = useTranslation();
  const pricing = useQuery<SalesResponse>("/customers/" + props.userId + "/pricing");
  const models = useQuery<ModelsResponse>("/models?limit=200");
  if (pricing.loading || models.loading) return <Dialog title={t("Customer sales pricing")} onClose={props.onClose}><Loading /></Dialog>;
  if (!pricing.data || !models.data) return <Dialog title={t("Customer sales pricing")} onClose={props.onClose}><ErrorNotice error={pricing.error || models.error} /></Dialog>;
  const salesData = pricing.data;
  const global = salesData.items.find((item) => item.is_global || (!item.model_key && !item.origin_model_name));
  const overrides = new Map(salesData.items.filter((item) => item.origin_model_name).map((item) => [item.origin_model_name, item]));
  const rows: CustomerSalesRow[] = models.data.items.map((model) => {
    const row = overrides.get(model);
    return { model, inheritedSalesBPS: row?.inherited_sales_bps ?? salesData.default_sales_bps, overrideSalesBPS: row?.override_sales_bps ?? null };
  });
  return <CustomerSalesMatrix key={`${props.userId}:${rows.map((row) => `${row.model}:${row.overrideSalesBPS ?? ""}`).join("|")}:${global?.override_sales_bps ?? ""}`} {...props} rows={rows} globalSalesBPS={global?.override_sales_bps ?? null} agencyDefaultSalesBPS={salesData.default_sales_bps} />;
}

function CustomerSalesMatrix(props: { userId: string | number; username: string; rows: CustomerSalesRow[]; globalSalesBPS: number | null; agencyDefaultSalesBPS: number; onClose: () => void }) {
  const { t } = useTranslation();
  const mutation = useMutation();
  const [search, setSearch] = useState("");
  const [page, setPage] = useState(0);
  const [globalValue, setGlobalValue] = useState(props.globalSalesBPS == null ? "" : formatCoefficient(props.globalSalesBPS));
  const [values, setValues] = useState<Record<string, string>>(() => Object.fromEntries(props.rows.map((row) => [row.model, row.overrideSalesBPS == null ? "" : formatCoefficient(row.overrideSalesBPS)])));
  const [reason, setReason] = useState("");
  const [error, setError] = useState<unknown>(null);
  const visibleRows = useMemo(() => { const query = search.trim().toLowerCase(); return query ? props.rows.filter((row) => row.model.toLowerCase().includes(query)) : props.rows; }, [props.rows, search]);
  const pageSize = 20;
  const pageCount = Math.max(1, Math.ceil(visibleRows.length / pageSize));
  const currentPage = Math.min(page, pageCount - 1);
  const rows = visibleRows.slice(currentPage * pageSize, (currentPage + 1) * pageSize);

  async function save() {
    if (!reason.trim()) return;
    setError(null);
    try {
      await mutation.mutate(
        "/customers/" + props.userId + "/pricing/batch",
        { global_sales_bps: globalValue.trim() ? parseCoefficient(globalValue) : null, models: props.rows.map((row) => ({ model_name: row.model, sales_bps: values[row.model]?.trim() ? parseCoefficient(values[row.model]) : null })), reason: reason.trim() },
        { method: "PUT", action: "pricing.customer_sales.batch_publish", objectId: "user:" + props.userId, title: t("Save customer pricing") },
      );
      props.onClose();
    } catch (cause) { setError(cause); }
  }

  return <Dialog title={t("Customer sales pricing") + " · " + props.username} onClose={props.onClose} busy={mutation.pending}>
    <p className="muted">{t("Set prices for this customer only. A blank model value inherits the agency sales coefficient shown in the table.")}</p>
    <div className="customer-pricing-summary">
      <Field label={t("Customer universal sales coefficient")} hint={t("Leave blank to inherit each model's agency sales coefficient.")}>
        <input inputMode="decimal" placeholder={formatCoefficient(props.agencyDefaultSalesBPS)} value={globalValue} onChange={(event) => setGlobalValue(event.target.value)} />
      </Field>
    </div>
    <div className="pricing-search customer-pricing-search">
      <Field label={t("Search models")}><input value={search} onChange={(event) => { setSearch(event.target.value); setPage(0); }} /></Field>
      <span className="pricing-live-badge">{t("Models")} · {visibleRows.length}</span>
    </div>
    <div className="table-wrap pricing-matrix-wrap customer-pricing-matrix-wrap">
      <table className="pricing-matrix customer-pricing-matrix">
        <thead><tr><th>{t("Model name")}</th><th>{t("Agency sales coefficient")}</th><th>{t("Customer sales coefficient")}</th></tr></thead>
        <tbody>{rows.map((row) => <tr key={row.model}><td><strong>{row.model}</strong></td><td><span className="coefficient-readonly">{formatCoefficient(row.inheritedSalesBPS)}</span></td><td><input aria-label={`${t("Customer sales coefficient")}: ${row.model}`} inputMode="decimal" placeholder={formatCoefficient(row.inheritedSalesBPS)} value={values[row.model] ?? ""} onChange={(event) => setValues((current) => ({ ...current, [row.model]: event.target.value }))} /><small>{values[row.model] ? t("Customer override") : t("Using agency coefficient {{value}}", { value: formatCoefficient(row.inheritedSalesBPS) })}</small></td></tr>)}</tbody>
      </table>
    </div>
    {visibleRows.length > pageSize && <Pager nextCursor={currentPage < pageCount - 1 ? String(currentPage + 1) : undefined} hasPrevious={currentPage > 0} onNext={() => setPage((value) => Math.min(value + 1, pageCount - 1))} onReset={() => setPage(0)} />}
    {!visibleRows.length && <p className="empty">{t("No models are available for customer pricing.")}</p>}
    <div className="pricing-publish-row"><Field label={t("Change reason")}><input required value={reason} maxLength={1000} onChange={(event) => setReason(event.target.value)} /></Field><button type="button" disabled={mutation.pending || !reason.trim()} onClick={() => void save()}>{t("Save customer pricing")}</button></div>
    <ErrorNotice error={error} />
    <div className="actions dialog-actions"><button type="button" className="secondary" onClick={props.onClose}>{t("Cancel")}</button></div>
  </Dialog>;
}
