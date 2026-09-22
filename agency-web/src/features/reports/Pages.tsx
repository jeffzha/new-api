import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ActionIcon, PageHeading } from "../../components/Heading";
import { PageHeader } from "../../components/PageHeader";
import { DataTable, ErrorNotice, Loading, Money, Pagination, Time } from "../../components/ui";
import { useQuery } from "../../lib/query";
import { useMutation } from "../../lib/mutations";
import type { Column, Identity, Page } from "../../lib/types";
import type { CommissionBalance } from "../finance/contracts";
import { CustomerManagementDialog } from "../customers/Management";
import { CustomerSalesEditor } from "../pricing/CustomerSalesEditor";
import { ReconciliationIssues } from "../reconciliation/Issues";
import { ReconciliationRuns } from "../reconciliation/Runs";

type Row = Record<string, string | number | null>;

const dynamicLabels: Record<string, string> = {
  admin_grant: "管理员调整",
  redemption: "兑换码充值",
  payment_self: "用户自主充值",
  payment_assisted: "管理员代充",
  payment_assisted_bonus: "代充赠送",
  payment_self_bonus: "用户充值赠送",
  payment_unattributed: "未归属支付",
  direct_customer: "直属客户佣金",
  child_agency_spread: "下级代理商差价佣金",
  unattributed_nonpaid: "未归属赠送",
  wallet: "钱包余额",
  debt: "欠费",
  reversal: "冲正",
  original: "原始记录",
  funding_account: "资金账户",
  commission_balance: "佣金余额",
  withdrawal_lock: "提现冻结",
  withdrawal: "提现单",
  withdrawal_account: "收款账户",
  user: "客户",
  user_binding: "客户归属",
  provisioning: "绑定任务",
  agency: "代理商",
  reconciliation_issue: "对账异常",
  operator_account: "代理商账号",
  export: "导出任务",
  billing_event: "计费事件",
  "agency.create": "新增代理商",
  "agency.update": "编辑代理商",
  "agency.enable": "启用代理商",
  "agency.disable": "停用代理商",
  "agency.password_reset": "重置代理商密码",
  "pricing.publish": "发布价格策略",
  "pricing.platform.publish": "发布平台价格策略",
  "pricing.sales.publish": "发布代理商销售系数",
  platform_pricing: "平台价格策略",
  "user.transfer": "调整客户归属",
  "provisioning.start": "开始绑定客户",
  "provisioning.completed": "完成客户绑定",
  "provisioning.cancel": "取消客户绑定",
  "withdrawal.create": "提交提现申请",
  "withdrawal.cancel": "取消提现申请",
  "withdrawal.transition": "更新提现状态",
  "withdrawal.review": "审核提现申请",
  "withdrawal.reject": "驳回提现申请",
  "withdrawal.recover_unpaid": "恢复未支付提现",
  "withdrawal.mark_paid": "确认提现已支付",
  "withdrawal_account.create": "新增收款账户",
  "withdrawal_account.update": "更新收款账户",
  "withdrawal_account.disable": "停用收款账户",
  "withdrawal_account.reveal": "查看收款账户",
  "export.create": "创建数据导出",
  "export.download": "下载数据导出",
  "reconciliation.run": "执行对账",
  "reconciliation.resolve": "处理对账异常",
  "delivery.ack": "确认安全信息送达",
  "funding.reverse": "资金冲正",
  "agency updated": "更新代理商资料",
  "agency created": "创建代理商账号",
  "password reset": "重置登录密码",
};

function dynamicLabel(value: unknown, fallback: string): string {
  const raw = String(value ?? "").trim();
  return dynamicLabels[raw] ?? (raw || fallback);
}

// Main-site quota is stored as USD-units scaled by quotaPerUnit. The default
// system setting is 500,000 quota units per USD and 7.3 CNY per USD. Agency
// reports intentionally show a stable RMB approximation for readable audit
// tables; provider receipts still retain their exact source amount.
const QUOTA_PER_YUAN = 500_000 / 7.3;
function fundingSourceLabel(value: unknown): string {
  const raw = String(value ?? "").trim();
  return {
    admin_grant: "超级管理员调整",
    redemption: "兑换码充值",
    payment_self: "用户自主充值",
    payment_assisted: "管理员代充",
    payment_self_bonus: "用户充值赠送",
    payment_unattributed: "未归属支付",
    unattributed_nonpaid: "未归属赠送",
    debt: "欠费",
    legacy_unknown: "历史记录",
  }[raw] ?? (raw || "未知来源");
}

function statusLabel(value: unknown): string {
  const raw = String(value ?? "").trim();
  return { success: "成功", pending: "处理中", failed: "失败", completed: "已完成", paid: "已支付" }[raw] ?? (raw || "未知状态");
}

export function formatYuan(value: unknown): string {
  if (value === null || value === undefined || value === "") return "—";
  const number = Number(value);
  if (!Number.isFinite(number)) return String(value);
  const yuan = number / QUOTA_PER_YUAN;
  const digits = Math.abs(yuan) > 0 && Math.abs(yuan) < 0.01 ? 6 : 2;
  return `¥${yuan.toLocaleString("zh-CN", {
    minimumFractionDigits: digits,
    maximumFractionDigits: digits,
  })}`;
}

export function formatExpiry(value: unknown): string {
  const number = Number(value);
  return !Number.isFinite(number) || number <= 0 ? "永不过期" : new Intl.DateTimeFormat("zh-CN", {
    timeZone: "Asia/Shanghai", dateStyle: "medium", timeStyle: "short",
  }).format(new Date(number < 1e12 ? number * 1000 : number));
}

function formatActualMoney(value: unknown, currency: unknown): string {
  if (value === null || value === undefined || value === "") return "—";
  const amount = Number(value);
  if (!Number.isFinite(amount)) return String(value);
  const code = String(currency ?? "CNY").toUpperCase();
  return `${code === "CNY" ? "¥" : code + " "}${amount.toFixed(2)}`;
}

function formatFundingBreakdown(value: unknown): string {
  if (!Array.isArray(value)) return "—";
  const parts = value.flatMap((entry) => {
    if (!entry || typeof entry !== "object") return [];
    const item = entry as { source?: unknown; quota?: unknown };
    const source = fundingSourceLabel(item.source);
    const quota = formatYuan(item.quota);
    return source !== "—" && quota !== "—" ? [`${source}: ${quota}`] : [];
  });
  return parts.length ? parts.join(" · ") : "—";
}
interface Customer {
  user_id: string | number;
  username: string;
  account_name?: string;
  agency_id?: string;
  agency_name?: string;
  agency_account?: string;
  effective_at_ms: string;
  revision: string;
}
interface SyncStatus {
  schema: { ready: boolean };
  capabilities: Record<string, boolean>;
  backlog: {
    deliveries: Record<string, number>;
    open_reconciliation_issues: number;
    exports_in_progress: number;
  };
}

export function OverviewPage({ identity }: { identity: Identity }) {
  const { t } = useTranslation();
  const balances = useQuery<{ items: CommissionBalance[] }>(
    identity.agency_id ? "/commissions/summary" : null,
  );
  if (!identity.agency_id) return <SyncStatusView />;
  return (
    <section>
      <PageHeader icon="overview" title={t("Overview")} description={t("Review commission balances and account performance at a glance.")} actions={<button type="button" className="secondary button-icon" onClick={balances.reload}>
          <ActionIcon name="refresh" />
          {t("Refresh")}
        </button>} />
      <ErrorNotice error={balances.error} />
      {balances.loading && <Loading />}
      {!balances.loading && !balances.error && !balances.data?.items.length && (
        <p className="empty">{t("No commission has been recorded yet.")}</p>
      )}
      {balances.data?.items.map((balance) => (
        <section key={balance.currency_code}>
          <h3>{balance.currency_code}</h3>
          <div className="metrics commission-metrics">
            {(
              [
                { key: "available_micros", label: "Available commission" },
                { key: "tax_micros", label: "Tax" },
                { key: "withdrawable_micros", label: "Withdrawable amount" },
                { key: "paid_micros", label: "Withdrawn commission" },
                { key: "net_earned_micros", label: "Net total commission" },
                { key: "earned_micros", label: "Total earned commission" },
                { key: "reversed_micros", label: "Reversed commission" },
                { key: "locked_micros", label: "Locked commission" },
              ] as const
            ).map(({ key, label }) => (
              <article key={key}>
                <span>{t(label)}</span>
                <strong>
                  <Money value={balance[key]} currency={balance.currency_code} />
                </strong>
              </article>
            ))}
          </div>
        </section>
      ))}
      {Boolean(balances.data?.items.length) && (
        <p className="muted">
          {t(
            "Net total commission equals total earned minus reversed commission. Withdrawn and locked amounts are included in this total. Each currency is calculated separately.",
          )}
        </p>
      )}
    </section>
  );
}

export function CustomersPage({
  identity,
  onEnter,
}: {
  identity: Identity;
  onEnter: (id: string) => Promise<void>;
}) {
  const { t } = useTranslation();
  const [cursor, setCursor] = useState("");
  const [selected, setSelected] = useState<Customer | null>(null);
  const [management, setManagement] = useState<{ username?: string } | null>(null);
  const [salesCustomer, setSalesCustomer] = useState<Customer | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const global = identity.actor_type === "root" && !identity.agency_id;
  const query = useQuery<Page<Customer>>(
    `${global ? "/root" : ""}/customers?page_size=30&cursor=${encodeURIComponent(cursor)}`,
  );
  if (salesCustomer) {
    return <CustomerSalesEditor
      userId={salesCustomer.user_id}
      username={salesCustomer.username}
      onBack={() => setSalesCustomer(null)}
    />;
  }
  return (
    <section>
      <PageHeader icon="customers" title={t("Customers")} description={t("View customer accounts, ownership and usage activity.")} actions={<>
        {identity.actor_type === "root" && (
          <button className="button-icon" type="button" onClick={() => setManagement({})}>
            <ActionIcon name="user" />
            {t("Customer assignment")}
          </button>
        )}
        <button type="button" className="secondary button-icon" onClick={query.reload}>
          <ActionIcon name="refresh" />
          {t("Refresh")}
        </button></>} />
      <ErrorNotice error={query.error || error} />
      {management && (
        <CustomerManagementDialog
          username={management.username}
          onClose={() => setManagement(null)}
          onChanged={query.reload}
        />
      )}
      {selected ? (
        <>
          <button className="secondary button-icon customer-back" type="button" onClick={() => setSelected(null)}>
            <ActionIcon name="arrow-left" />
            {t("Back to customers")}
          </button>
          <CustomerHistory key={String(selected.user_id)} customer={selected} />
        </>
      ) : (
        <>
          {query.loading ? (
            <Loading />
          ) : (
            <DataTable
              rows={query.data?.items || []}
              rowKey={(row) => String(row.user_id)}
              columns={[
                { key: "account_name", label: "Customer account" },
                ...(global ? [{
                  key: "agency_name",
                  label: "Agency",
                  render: (row: Customer) => row.agency_account
                    ? `${row.agency_name}（${row.agency_account}）`
                    : row.agency_name || "—",
                }] : []),
                {
                  key: "effective_at_ms",
                  label: "Bound at",
                  render: (row) => <Time value={row.effective_at_ms} />,
                },
              ]}
              actions={(row) => (
                <>
                  {identity.actor_type === "root" && (
                    <button
                      type="button"
                      className="secondary button-icon compact-action"
                      onClick={() => setManagement({ username: row.username })}
                    >
                      <ActionIcon name="edit" />
                      {t("Manage assignment")}
                    </button>
                  )}
                  {!global && (
                    <button
                      type="button"
                      className="secondary button-icon compact-action"
                      onClick={() => setSalesCustomer(row)}
                    >
                      <ActionIcon name="edit" />
                      {t("Customer pricing")}
                    </button>
                  )}
                  <button
                    type="button"
                    className="secondary button-icon compact-action"
                    disabled={busy}
                    onClick={() => {
                      if (!global) {
                        setSelected(row);
                        return;
                      }
                      setBusy(true);
                      setError(null);
                      void onEnter(String(row.agency_id))
                        .catch(setError)
                        .finally(() => setBusy(false));
                    }}
                  >
                    <ActionIcon name={global ? "play" : "eye"} />
                    {t(global ? "Enter agency" : "View usage and top-ups")}
                  </button>
                </>
              )}
            />
          )}
          <Pagination
            hasPrevious={Boolean(cursor)}
            nextCursor={query.data?.meta?.next_cursor}
            onNext={() => setCursor(query.data?.meta?.next_cursor || "")}
            onReset={() => setCursor("")}
          />
        </>
      )}
    </section>
  );
}

function CustomerHistory({ customer }: { customer: Customer }) {
  const { t } = useTranslation();
  const [tab, setTab] = useState<"usage" | "topups">("usage");
  const usageColumns: Column<Row>[] = [
    {
      key: "occurred_at_ms",
      label: "Time",
      render: (row) => <Time value={row.occurred_at_ms || undefined} />,
    },
    { key: "model", label: "Model" },
    { key: "endpoint", label: "Endpoint" },
    { key: "business_status", label: "Status", render: (row) => statusLabel(row.business_status) },
    { key: "input_tokens", label: "Input tokens" },
    { key: "output_tokens", label: "Output tokens" },
    { key: "cache_read_tokens", label: "Cache read tokens" },
    { key: "cache_write_tokens", label: "Cache write tokens" },
    { key: "standard_quota", label: "Standard quota", render: (row) => formatYuan(row.standard_quota) },
    { key: "sales_bps", label: "Sales coefficient", render: (row) => row.sales_bps == null ? "—" : `${Number(row.sales_bps) / 100}%` },
    { key: "charged_quota", label: "Charged amount", render: (row) => formatYuan(row.charged_quota) },
    { key: "paid_quota", label: "Paid amount", render: (row) => formatYuan(row.paid_quota) },
    { key: "nonpaid_quota", label: "Non-paid amount", render: (row) => formatYuan(row.nonpaid_quota) },
    { key: "debt_quota", label: "Debt amount", render: (row) => formatYuan(row.debt_quota) },
    {
      key: "funding_breakdown",
      label: "Funding allocation",
      render: (row) => formatFundingBreakdown(row.funding_breakdown),
    },
    { key: "skip_reason", label: "Commission exclusion reason" },
  ];
  const topupColumns: Column<Row>[] = [
    {
      key: "occurred_at_ms",
      label: "Time",
      render: (row) => <Time value={row.occurred_at_ms || undefined} />,
    },
    { key: "funding_source", label: "Funding source", render: (row) => fundingSourceLabel(row.funding_source) },
    { key: "initiated_by", label: "Initiated by", render: (row) => String(row.initiated_by ?? "系统") },
    { key: "actual_money", label: "Actual payment", render: (row) => formatActualMoney(row.actual_money, row.currency_code) },
    { key: "currency_code", label: "Currency", render: () => "人民币" },
    { key: "credited_quota", label: "Credited amount", render: (row) => formatYuan(row.credited_quota) },
    { key: "paid_quota", label: "Paid amount", render: (row) => formatYuan(row.paid_quota) },
    { key: "bonus_quota", label: "Bonus amount", render: (row) => formatYuan(row.bonus_quota) },
    { key: "consumed_quota", label: "Consumed amount", render: (row) => formatYuan(row.consumed_quota) },
    { key: "remaining_quota", label: "Remaining amount", render: (row) => formatYuan(row.remaining_quota) },
    { key: "expired_quota", label: "Expired amount", render: (row) => formatYuan(row.expired_quota) },
    {
      key: "expires_at",
      label: "Expires at",
      render: (row) => formatExpiry(row.expires_at),
    },
    { key: "refunded_quota", label: "Refunded amount", render: (row) => formatYuan(row.refunded_quota) },
    { key: "completion_source", label: "Completion source", render: (row) => fundingSourceLabel(row.completion_source) },
    { key: "payment_status", label: "Payment status", render: (row) => statusLabel(row.payment_status) },
    { key: "payment_reference", label: "Payment reference" },
  ];
  return (
    <section>
      <PageHeading icon="customers" level={3}>
        客户：{customer.username}
      </PageHeading>
      <div className="tabs">
        <button
          type="button"
          className={tab === "usage" ? "tab active" : "tab"}
          onClick={() => setTab("usage")}
        >
          {t("Usage")}
        </button>
        <button
          type="button"
          className={tab === "topups" ? "tab active" : "tab"}
          onClick={() => setTab("topups")}
        >
          {t("Top-ups")}
        </button>
      </div>
      <PagedReport
        key={tab}
        path={`/customers/${customer.user_id}/${tab}`}
        columns={tab === "usage" ? usageColumns : topupColumns}
      />
    </section>
  );
}

function PagedReport({ path, columns }: { path: string; columns: Column<Row>[] }) {
  const { t } = useTranslation();
  const [cursor, setCursor] = useState("");
  const query = useQuery<Page<Row>>(`${path}?page_size=30&cursor=${encodeURIComponent(cursor)}`);
  return (
    <>
      <div className="toolbar">
        <button type="button" className="secondary button-icon" onClick={query.reload}>
          <ActionIcon name="refresh" />
          {t("Refresh")}
        </button>
      </div>
      <ErrorNotice error={query.error} />
      {query.loading ? <Loading /> : <DataTable rows={query.data?.items || []} columns={columns} />}
      <Pagination
        hasPrevious={Boolean(cursor)}
        nextCursor={query.data?.meta?.next_cursor}
        onNext={() => setCursor(query.data?.meta?.next_cursor || "")}
        onReset={() => setCursor("")}
      />
    </>
  );
}

export function LedgerPage({ onExport }: { onExport?: () => void }) {
  const { t } = useTranslation();
  return (
    <section>
      <PageHeader icon="ledger" title={t("Commission ledger")} description={t("Track commission events and export detailed records.")} actions={<>{onExport && (
          <button type="button" className="secondary button-icon" onClick={onExport}>
            <ActionIcon name="download" />
            {t("Export commission details")}
          </button>
        )}</>} />
      <PagedReport
        path="/commissions/ledger"
        columns={[
          {
            key: "occurred_at_ms",
            label: "Time",
            render: (row) => <Time value={row.occurred_at_ms || undefined} />,
          },
          { key: "account_name", label: "Customer account" },
          { key: "agency_name", label: "Serving agency" },
          { key: "commission_source", label: "Commission source", render: (row) => dynamicLabel(row.commission_source, t("Unknown")) },
          { key: "origin_model_name", label: "Public model" },
          { key: "entry_type", label: "Entry type", render: (row) => dynamicLabel(row.entry_type, t("Unknown")) },
          {
            key: "amount_micros",
            label: "Commission",
            render: (row) => (
              <Money value={row.amount_micros || "0"} currency={String(row.currency_code)} />
            ),
          },
          { key: "commission_quota", label: "Commission quota" },
          { key: "paid_allocated_quota", label: "Paid funding quota" },
          { key: "event_id", label: "Event ID" },
        ]}
      />
    </section>
  );
}

export function AuditPage({ root }: { root: boolean }) {
  const { t } = useTranslation();
  return (
    <section>
      <PageHeader icon="audit" title={t("Audit log")} description={t("Review sensitive actions and operational changes.")} />
      <PagedReport
        path={root ? "/root/audit" : "/audit"}
        columns={[
          {
            key: "created_at_ms",
            label: "Time",
            render: (row) => (
              <Time value={row.created_at_ms || undefined} />
            ),
          },
          { key: "action", label: "Action", render: (row) => dynamicLabel(row.action, t("Unknown")) },
          { key: "object_type", label: "Object type", render: (row) => dynamicLabel(row.object_type, t("Unknown")) },
          { key: "object_name", label: "Account name", render: (row) => String(row.object_name || row.object_id || t("Unknown")) },
          { key: "reason", label: "Reason", render: (row) => dynamicLabel(row.reason, "—") },
          { key: "request_id", label: "Request ID" },
        ]}
      />
    </section>
  );
}

const capabilityNames: Record<string, string> = {
  agency_durable_v1: "Agency data service",
  billing_component_v2: "Billing component",
  billing_schemas: "Billing data schema",
  commission_worker: "Commission processing",
  exports: "Data exports",
  fact_projection: "Usage projection",
  onboarding: "Agency onboarding",
  outbox_v1: "Message queue",
  pricing_snapshot_v1: "Pricing snapshot",
  withdrawals: "Commission withdrawals",
};
const queueNames: Record<string, string> = {
  claimed: "Processing",
  pending: "Pending",
  poison: "Failed jobs",
  retry: "Retrying",
  open_reconciliation_issues: "Open reconciliation",
  exports_in_progress: "Exports in progress",
};

function SyncStatusView() {
  const { t } = useTranslation();
  const query = useQuery<SyncStatus>("/root/sync/status");
  const enabledCount = query.data
    ? Object.values(query.data.capabilities).filter(Boolean).length
    : 0;
  const capabilityCount = query.data ? Object.keys(query.data.capabilities).length : 0;
  const capabilityPercent = capabilityCount ? Math.round((enabledCount / capabilityCount) * 100) : 0;
  const pendingCount = query.data
    ? Object.values(query.data.backlog.deliveries || {}).reduce(
        (sum, value) => sum + Number(value || 0),
        0,
      )
    : 0;
  return (
    <section>
      <PageHeader icon="status" title={t("Service status")} description={t("Live health checks for services, queues and data")} actions={<button type="button" className="secondary button-icon" onClick={query.reload}>
          <ActionIcon name="refresh" />
          {t("Refresh")}
        </button>} />
      <ErrorNotice error={query.error} />
      {query.loading && <Loading />}
      {query.data && (
        <>
          <div className="service-health">
            <div className={query.data.schema.ready ? "health-banner ready" : "health-banner warning"}>
              <span className="health-dot" aria-hidden="true" />
              <div>
                <strong>{t(query.data.schema.ready ? "All systems operational" : "Schema needs attention")}</strong>
                <p>{t(query.data.schema.ready ? "Database schema is ready." : "Database schema is incomplete.")}</p>
              </div>
            </div>
            <div className="status-kpis">
              <div className="status-kpi"><span>{t("Enabled capabilities")}</span><strong>{enabledCount}<small> / {capabilityCount}</small></strong><div className="kpi-progress"><span style={{ width: `${capabilityPercent}%` }} /></div></div>
              <div className="status-kpi"><span>{t("Pending work")}</span><strong>{pendingCount}</strong><small className="kpi-caption">{pendingCount === 0 ? t("All queues are clear") : t("Needs attention")}</small></div>
              <div className="status-kpi"><span>{t("Open issues")}</span><strong>{query.data.backlog.open_reconciliation_issues || 0}</strong></div>
            </div>
          </div>
          <div className="status-section">
            <div className="section-heading"><div><PageHeading icon="agency" level={3}>{t("Capability")}</PageHeading><p className="muted">{t("Live availability of agency services")}</p></div></div>
            <div className="capability-grid">
              {Object.entries(query.data.capabilities).map(([name, value]) => (
                <article className={value ? "capability-card enabled" : "capability-card disabled"} key={name}>
                  <div className="capability-icon" aria-hidden="true">{value ? "✓" : "!"}</div>
                  <div><strong>{t(capabilityNames[name] || name.replaceAll("_", " ") )}</strong><span>{t(value ? "Enabled" : "Disabled")}</span></div>
                </article>
              ))}
            </div>
          </div>
          <div className="status-section">
            <div className="section-heading"><div><PageHeading icon="sync" level={3}>{t("Pending work")}</PageHeading><p className="muted">{t("Background jobs and synchronization queues")}</p></div></div>
            <div className="queue-grid">
              {Object.entries({
                ...(query.data.backlog.deliveries || {}),
                open_reconciliation_issues: query.data.backlog.open_reconciliation_issues,
                exports_in_progress: query.data.backlog.exports_in_progress,
              }).map(([name, value]) => (
                <article className={Number(value) > 0 ? "queue-card has-items" : "queue-card"} key={name}>
                  <span>{t(queueNames[name] || name.replaceAll("_", " ") )}</span>
                  <strong>{Number(value || 0).toLocaleString()}</strong>
                  <small>{t(Number(value || 0) > 0 ? "Needs attention" : "Clear")}</small>
                </article>
              ))}
            </div>
          </div>
        </>
      )}
    </section>
  );
}

export function SyncPage() {
  const { t } = useTranslation();
  const mutation = useMutation();
  const [error, setError] = useState<unknown>(null);
  const [revision, setRevision] = useState(0);
  const [statusRevision, setStatusRevision] = useState(0);
  async function run() {
    setError(null);
    try {
      await mutation.mutate(
        "/root/reconciliation/runs",
        {},
        {
          action: "reconciliation.run",
          objectId: "reconciliation:run",
          title: t("Run reconciliation"),
        },
      );
      setRevision((value) => value + 1);
    } catch (cause) {
      setError(cause);
    }
  }
  return (
    <section>
      <SyncStatusView key={`${revision}:${statusRevision}`} />
      <div className="reconciliation-page-header">
      <PageHeader icon="sync" title={t("Reconciliation")} description={t("Run checks to compare current records and resolve discrepancies.")} actions={<button className="button-icon" type="button" disabled={mutation.pending} onClick={() => void run()}>
          <ActionIcon name="refresh" />
          {t("Run reconciliation")}
        </button>} />
      </div>
      <p className="muted">
        {t(
          "A reconciliation run checks current records. Its recorded cutoff is not a historical snapshot.",
        )}
      </p>
      <ErrorNotice error={error} />
      <ReconciliationIssues
        key={revision}
        onChanged={() => setStatusRevision((value) => value + 1)}
      />
      <ReconciliationRuns key={`runs:${revision}`} />
    </section>
  );
}
