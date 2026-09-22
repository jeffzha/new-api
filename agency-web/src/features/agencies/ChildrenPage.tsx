import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ActionIcon } from "../../components/Heading";
import { PageHeader } from "../../components/PageHeader";
import { Dialog, ErrorNotice, Field, Loading, Pager, Table, Time } from "../../components/ui";
import { useMutation } from "../../lib/mutations";
import { useQuery } from "../../lib/client";
import { formatCoefficient, parseCoefficient } from "../pricing/policy";

type Child = { id: number | string; display_name: string; operator_username?: string; status: string; depth: number; price_revision: number };
type ChildrenResponse = { items: Child[] };
type HierarchyResponse = { current: Child; parents: Child[]; children: Child[] };
type PasswordDelivery = { temporary_password?: string; invite_url?: string; invite_qr_url?: string; temporary_password_expires_at?: number; operator_username?: string };
type ModelCostOverride = { origin_model_name: string; settlement_bps?: number | null };
type Pricing = { revision: number; default_settlement_bps: number; model_overrides?: ModelCostOverride[] };

export function ChildrenPage() {
  const { t } = useTranslation();
  const query = useQuery<ChildrenResponse>("/children");
  const hierarchy = useQuery<HierarchyResponse>("/hierarchy");
  const pricing = useQuery<Pricing>("/pricing");
  const [create, setCreate] = useState(false);
  const [delivery, setDelivery] = useState<PasswordDelivery | null>(null);
  const [selected, setSelected] = useState<Child | null>(null);
  const [page, setPage] = useState(0);
  if (query.loading || pricing.loading || hierarchy.loading) return <Loading />;
  const all = query.data?.items ?? [];
  const pageSize = 20;
  const pageCount = Math.max(1, Math.ceil(all.length / pageSize));
  const currentPage = Math.min(page, pageCount - 1);
  const rows = all.slice(currentPage * pageSize, (currentPage + 1) * pageSize);
  async function refresh() { await Promise.all([query.reload(), hierarchy.reload(), pricing.reload()]); }
  return <section>
    <PageHeader icon="agency" title={t("Agencies")} description={t("Create and manage your direct child agencies. You cannot edit grandchildren or upstream agencies.")} actions={<><button type="button" className="button-icon compact-action" onClick={() => setCreate(true)}><ActionIcon name="plus" />{t("Create child agency")}</button><button type="button" className="secondary button-icon compact-action" onClick={() => void refresh()}><ActionIcon name="refresh" />{t("Refresh")}</button></>} />
    <ErrorNotice error={query.error || pricing.error || hierarchy.error} />
    {hierarchy.data && <HierarchySummary data={hierarchy.data} />}
    {create && pricing.data && <ChildForm parent={pricing.data} onClose={() => setCreate(false)} onCreated={(result) => { setCreate(false); setDelivery(result); void refresh(); }} />}
    {delivery && <ChildDelivery delivery={delivery} onClose={() => setDelivery(null)} />}
    {selected && <ChildPricing child={selected} onClose={() => setSelected(null)} onSaved={() => void refresh()} />}
    <Table rows={rows} rowKey={(row) => String(row.id)} columns={[
      { key: "display_name", label: t("Agency name") },
      { key: "operator_username", label: t("Operator username") },
      { key: "depth", label: t("Hierarchy depth") },
      { key: "status", label: t("Status"), render: (row) => t(row.status === "active" ? "Active" : "Disabled") },
      { key: "price_revision", label: t("Price revision") },
    ]} actions={(row) => <button type="button" className="secondary button-icon compact-action" disabled={row.status !== "active"} onClick={() => setSelected(row)}><ActionIcon name="save" />{t("Manage child pricing")}</button>} />
    <div className="list-pagination">
      <span>{t("Showing {{from}}–{{to}} of {{total}} agencies", { from: all.length ? currentPage * pageSize + 1 : 0, to: Math.min((currentPage + 1) * pageSize, all.length), total: all.length })}</span>
      <Pager nextCursor={currentPage < pageCount - 1 ? String(currentPage + 1) : undefined} hasPrevious={currentPage > 0} onNext={() => setPage((value) => Math.min(value + 1, pageCount - 1))} onReset={() => setPage(0)} />
    </div>
  </section>;
}

function HierarchySummary(props: { data: HierarchyResponse }) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  const path = [...props.data.parents].reverse().concat(props.data.current).map((item) => item.display_name).join(" → ");
  async function copyPath() { try { await navigator.clipboard.writeText(path); setCopied(true); } catch { setCopied(false); } }
  const upstream = [...props.data.parents].reverse();
  const childCount = props.data.children.length;
  return <section className="hierarchy-card" aria-label={t("Agency hierarchy")}>
    <div className="hierarchy-card-header"><div><h3>{t("Agency hierarchy")}</h3><p className="muted">{t("View the upstream path and direct child agencies for the current account.")}</p></div><button type="button" className="secondary button-icon compact-action" onClick={() => void copyPath()}><ActionIcon name="save" />{t(copied ? "Copied" : "Copy hierarchy")}</button></div>
    <div className="hierarchy-org-chart" role="tree" aria-label={t("Agency hierarchy")}>
      {upstream.length > 0 && <div className="hierarchy-ancestry"><span className="hierarchy-lane-label">{t("Upstream agencies")}</span><div className="hierarchy-parent-chain">{upstream.map((item) => <HierarchyNode key={item.id} item={item} label={t("Parent agency")} />)}</div></div>}
      <div className="hierarchy-focus-stage"><span className="hierarchy-lane-label">{t("Current agency")}</span><HierarchyNode item={props.data.current} label={t("Current agency")} current /></div>
      <div className="hierarchy-descendants"><div className="hierarchy-descendants-title"><span className="hierarchy-lane-label">{t("Direct child agencies")}</span>{childCount > 0 && <span className="hierarchy-count">{childCount}</span>}</div>{childCount > 0 ? <div className={`hierarchy-child-grid hierarchy-child-grid--${Math.min(childCount, 5)}`}>{props.data.children.map((item) => <HierarchyNode key={item.id} item={item} label={t("Direct child agency")} child />)}</div> : <p className="hierarchy-empty">{t("No direct child agencies yet.")}</p>}</div>
    </div>
    <p className="hierarchy-path"><strong>{t("Upstream path")}</strong>: {path || props.data.current.display_name}</p>
  </section>;
}

function HierarchyNode(props: { item: Child; label: string; current?: boolean; child?: boolean }) {
  const { t } = useTranslation();
  const initial = props.item.display_name.trim().slice(0, 1).toUpperCase() || "A";
  const status = t(props.item.status === "active" ? "Active" : "Disabled");
  return <div className={["agency-tree-node", props.current ? "current" : "", props.child ? "child" : ""].filter(Boolean).join(" ")} role="treeitem" aria-current={props.current ? "true" : undefined} aria-label={`${props.label}: ${props.item.display_name}, ${status}`}><div className="agency-tree-node-copy"><span className="agency-tree-avatar" aria-hidden="true">{initial}</span><div className="agency-tree-node-main"><strong>{props.item.display_name}</strong>{props.item.operator_username && <span className="agency-tree-account">@{props.item.operator_username}</span>}</div><span className={["agency-tree-status", props.item.status === "active" ? "active" : "disabled"].join(" ")}><i aria-hidden="true" />{status}</span><span className="agency-tree-role">{props.label}</span></div></div>;
}

function ChildForm(props: { parent: Pricing; onClose: () => void; onCreated: (result: PasswordDelivery) => void }) {
  const { t } = useTranslation();
  const mutation = useMutation();
  const [name, setName] = useState("");
  const [username, setUsername] = useState("");
  const [error, setError] = useState<unknown>(null);
  async function save() { try { const result = await mutation.mutate<PasswordDelivery>("/children", { display_name: name.trim(), operator_username: username.trim() }, { action: "agency.child.create", objectId: "agency:children", title: t("Create child agency") }); props.onCreated({ ...result, operator_username: username.trim() }); } catch (cause) { setError(cause); } }
  return <Dialog title={t("Create child agency")} onClose={props.onClose} busy={mutation.pending}><p className="muted">{t("Pricing is inherited from the parent agency. The parent must configure a child agency cost before creating the next level.")}</p><Field label={t("Agency name")}><input value={name} onChange={(event) => setName(event.target.value)} /></Field><Field label={t("Operator username")}><input value={username} onChange={(event) => setUsername(event.target.value)} /></Field><ErrorNotice error={error} /><div className="actions dialog-actions"><button className="secondary" type="button" onClick={props.onClose}>{t("Cancel")}</button><button type="button" disabled={mutation.pending || !name.trim() || !username.trim()} onClick={() => void save()}>{t("Create child agency")}</button></div></Dialog>;
}

function ChildDelivery(props: { delivery: PasswordDelivery; onClose: () => void }) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState<"password" | "invite" | null>(null);
  async function copy(value: string, kind: "password" | "invite") {
    try { await navigator.clipboard.writeText(value); setCopied(kind); } catch { setCopied(null); }
  }
  return <Dialog title={t("Child agency login details")} onClose={props.onClose}><p className="notice">{t("Save these details now. The temporary password is shown only once and must be changed at first sign-in.")}</p><div className="credential-grid"><Field label={t("Operator username")}><input readOnly value={props.delivery.operator_username || ""} /></Field><Field label={t("Temporary password")}><input readOnly value={props.delivery.temporary_password || ""} /></Field></div>{props.delivery.invite_url && <Field label={t("Invitation link")}><input readOnly value={props.delivery.invite_url} /></Field>}<div className="credential-delivery-footer"><div>{props.delivery.temporary_password_expires_at && <p>{t("Delivery expires at")}: <Time value={props.delivery.temporary_password_expires_at} /></p>}{copied === "password" && <p className="success" role="status">{t("Temporary password copied.")}</p>}{copied === "invite" && <p className="success" role="status">{t("Invitation link copied.")}</p>}</div><div className="actions"><button type="button" className="button-icon compact-action" onClick={() => void copy(props.delivery.temporary_password || "", "password")}><ActionIcon name="save" />{t(copied === "password" ? "Copied" : "Copy password")}</button>{props.delivery.invite_url && <button type="button" className="secondary button-icon compact-action" onClick={() => void copy(props.delivery.invite_url || "", "invite")}><ActionIcon name="save" />{t(copied === "invite" ? "Copied" : "Copy invitation link")}</button>}<button className="secondary compact-action" type="button" onClick={props.onClose}>{t("Close")}</button></div></div>{props.delivery.invite_qr_url && <div className="credential-qr"><p>{t("Invitation QR code")}</p><img src={props.delivery.invite_qr_url} alt={t("Invitation QR code")} /></div>}</Dialog>;
}

function ChildPricing(props: { child: Child; onClose: () => void; onSaved: () => void }) {
  const { t } = useTranslation();
  const query = useQuery<Pricing>(`/children/${props.child.id}/pricing`);
  const mutation = useMutation();
  const [cost, setCost] = useState(""); const [modelCosts, setModelCosts] = useState<Record<string, string>>({}); const [reason, setReason] = useState(""); const [error, setError] = useState<unknown>(null);
  if (query.loading || !query.data) return <Dialog title={t("Manage child pricing")} onClose={props.onClose}><Loading /><ErrorNotice error={query.error} /></Dialog>;
  const data = query.data; const modelOverrides = data.model_overrides ?? [];
  async function save() { if (!reason.trim()) return; try { const overrides = modelOverrides.flatMap((row) => { const value = modelCosts[row.origin_model_name] ?? (row.settlement_bps == null ? "" : formatCoefficient(row.settlement_bps)); return value.trim() ? [{ origin_model_name: row.origin_model_name, settlement_bps: parseCoefficient(value) }] : []; }); await mutation.mutate(`/children/${props.child.id}/pricing/publish`, { expected_revision: data.revision, default_settlement_bps: parseCoefficient(cost || formatCoefficient(data.default_settlement_bps)), model_cost_overrides: overrides, reason: reason.trim() }, { action: "agency.child.pricing.publish", objectId: `agency:${props.child.id}`, title: t("Manage child pricing") }); props.onSaved(); props.onClose(); } catch (cause) { setError(cause); } }
  return <Dialog title={t("Manage child pricing") + " · " + props.child.display_name} onClose={props.onClose} busy={mutation.pending}><p className="muted">{t("Only direct child cost can be changed. Sales pricing remains managed by the child agency for its customers.")}</p><Field label={t("Child cost coefficient")}><input inputMode="decimal" value={cost || formatCoefficient(data.default_settlement_bps)} onChange={(event) => setCost(event.target.value)} /></Field>{modelOverrides.length > 0 && <fieldset className="form-grid"><legend>{t("Model cost overrides")}</legend>{modelOverrides.map((row) => <Field key={row.origin_model_name} label={row.origin_model_name}><input inputMode="decimal" value={modelCosts[row.origin_model_name] ?? (row.settlement_bps == null ? "" : formatCoefficient(row.settlement_bps))} onChange={(event) => setModelCosts((current) => ({ ...current, [row.origin_model_name]: event.target.value }))} /></Field>)}</fieldset>}<Field label={t("Change reason")}><input required maxLength={2000} value={reason} onChange={(event) => setReason(event.target.value)} /></Field><ErrorNotice error={error} /><div className="actions dialog-actions"><button className="secondary" type="button" onClick={props.onClose}>{t("Cancel")}</button><button type="button" disabled={mutation.pending || !reason.trim()} onClick={() => void save()}>{t("Publish child pricing")}</button></div></Dialog>;
}
