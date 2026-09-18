import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { ActionIcon } from "./components/Heading";
import { api, ApiError } from "./lib/api";
import type { Identity } from "./lib/types";
import { useMutation } from "./lib/mutation-context";
import { MutationProvider } from "./components/MutationProvider";
import { Login, PasswordChange } from "./components/Auth";
import { ErrorNotice, Loading } from "./components/ui";
import { AgenciesPage } from "./features/agencies/AgenciesPage";
import { PricingPage } from "./features/pricing/PricingPage";
import { WithdrawalsPage } from "./features/finance/WithdrawalsPage";
import { AccountsPage } from "./features/finance/AccountsPage";
import { ExportsPage } from "./features/exports/ExportsPage";
import type { ExportKind } from "./features/exports/contracts";
import { InvitationsPage } from "./features/invitations/InvitationsPage";
import {
  CustomersPage,
  LedgerPage,
  OverviewPage,
  AuditPage,
  SyncPage,
} from "./features/reports/Pages";

export function App() {
  const [identity, setIdentity] = useState<Identity | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<unknown>(null);
  const { t } = useTranslation();
  const refresh = useCallback(async () => {
    const current = await api<Identity>("/auth/me");
    setIdentity(current);
    setError(null);
  }, []);
  useEffect(() => {
    const controller = new AbortController();
    api<Identity>("/auth/me", { signal: controller.signal })
      .then(setIdentity)
      .catch((cause) => {
        if (!controller.signal.aborted && !(cause instanceof ApiError && cause.status === 401))
          setError(cause);
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, []);
  if (loading)
    return (
      <main className="shell">
        <Loading />
      </main>
    );
  if (!identity)
    return (
      <>
        {error && (
          <div className="shell">
            <ErrorNotice error={error} />
          </div>
        )}
        <Login done={refresh} />
      </>
    );
  async function logout() {
    await api("/auth/logout", { method: "POST", body: "{}" });
    setIdentity(null);
  }
  if (identity.must_change_password)
    return (
      <main className="shell">
        <section className="auth">
          <PasswordChange required done={refresh} />
          <button className="secondary button-icon" type="button" onClick={() => void logout().catch(setError)}>
            <ActionIcon name="close" />
            {t("Sign out")}
          </button>
          <ErrorNotice error={error} />
        </section>
      </main>
    );
  return (
    <MutationProvider
      key={`${identity.actor_type}:${identity.actor_id}:${identity.agency_id || ""}`}
      identity={identity}
    >
      <Dashboard identity={identity} refresh={refresh} logout={logout} />
    </MutationProvider>
  );
}

type Tab =
  | "overview"
  | "agencies"
  | "pricing"
  | "customers"
  | "invitation"
  | "ledger"
  | "withdrawals"
  | "accounts"
  | "exports"
  | "sync"
  | "audit"
  | "security";
const tabLabels: Record<Tab, string> = {
  overview: "Overview",
  agencies: "Agencies",
  pricing: "Pricing",
  customers: "Customers",
  invitation: "Invite customers",
  ledger: "Commission ledger",
  withdrawals: "Withdrawals",
  accounts: "Payout accounts",
  exports: "Data exports",
  sync: "Sync & reconciliation",
  audit: "Audit log",
  security: "Account security",
};

function NavIcon({ tab }: { tab: Tab }) {
  const common = {
    width: 18,
    height: 18,
    viewBox: "0 0 24 24",
    fill: "none",
    stroke: "currentColor",
    strokeWidth: 1.8,
    strokeLinecap: "round" as const,
    strokeLinejoin: "round" as const,
    "aria-hidden": true,
  };
  switch (tab) {
    case "overview":
      return <svg {...common}><rect x="3" y="3" width="7" height="7" rx="1" /><rect x="14" y="3" width="7" height="7" rx="1" /><rect x="3" y="14" width="7" height="7" rx="1" /><rect x="14" y="14" width="7" height="7" rx="1" /></svg>;
    case "agencies":
      return <svg {...common}><path d="M4 21v-8h16v8M7 13V4h10v9M2 21h20M9 8h6M9 11h6" /></svg>;
    case "pricing":
      return <svg {...common}><path d="M12 3v18M17 7.5c0-1.7-1.9-3-5-3S7 5.8 7 7.5 8.9 10 12 10s5 1.3 5 3-1.9 3-5 3-5-1.3-5-3" /></svg>;
    case "customers":
      return <svg {...common}><path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2M9 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8M22 21v-2a4 4 0 0 0-3-3.87M16 3.13a4 4 0 0 1 0 7.75" /></svg>;
    case "invitation":
      return <svg {...common}><path d="M12 5v14M5 12h14" /><rect x="3" y="3" width="18" height="18" rx="4" /></svg>;
    case "ledger":
      return <svg {...common}><path d="M6 3h12a2 2 0 0 1 2 2v16H6a3 3 0 0 1-3-3V6a3 3 0 0 1 3-3ZM3 18a3 3 0 0 1 3-3h14M8 7h7M8 11h5" /></svg>;
    case "withdrawals":
      return <svg {...common}><path d="M12 3v12M7 10l5 5 5-5M5 21h14" /></svg>;
    case "accounts":
      return <svg {...common}><rect x="3" y="5" width="18" height="14" rx="2" /><path d="M3 10h18M7 15h4" /></svg>;
    case "exports":
      return <svg {...common}><path d="M12 3v12M7 10l5 5 5-5M5 21h14" /><path d="M5 3h4M15 3h4" /></svg>;
    case "sync":
      return <svg {...common}><path d="M20 11a8 8 0 0 0-14.7-4L3 10M3 5v5h5M4 13a8 8 0 0 0 14.7 4L21 14M21 19v-5h-5" /></svg>;
    case "audit":
      return <svg {...common}><path d="M4 4h16v16H4zM8 8h8M8 12h8M8 16h5" /></svg>;
    case "security":
      return <svg {...common}><path d="M12 3 20 6v5c0 5-3.4 8.5-8 10-4.6-1.5-8-5-8-10V6l8-3Z" /><path d="m9 12 2 2 4-4" /></svg>;
  }
}

function Dashboard({
  identity,
  refresh,
  logout,
}: {
  identity: Identity;
  refresh: () => Promise<void>;
  logout: () => Promise<void>;
}) {
  const { t, i18n } = useTranslation();
  const root = identity.actor_type === "root";
  const [tab, setTab] = useState<Tab>("overview");
  const [exportKind, setExportKind] = useState<ExportKind>("usage");
  const [pricingAgency, setPricingAgency] = useState<string | null>(
    identity.agency_id ? String(identity.agency_id) : null,
  );
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const mutate = useMutation();
  const tabs: Tab[] = root
    ? [
        "overview",
        "agencies",
        "pricing",
        "customers",
        ...(identity.agency_id ? (["invitation", "ledger", "accounts", "exports"] as Tab[]) : []),
        "withdrawals",
        "sync",
        "audit",
      ]
    : [
        "overview",
        "customers",
        "invitation",
        "pricing",
        "ledger",
        "withdrawals",
        "accounts",
        "exports",
        "security",
        "audit",
      ];
  async function enter(id: string) {
    await mutate({ path: `/root/agencies/${id}/enter`, body: {} });
    await refresh();
  }
  async function leave() {
    setBusy(true);
    setError(null);
    try {
      await mutate({ path: "/root/leave-agency", body: {} });
      await refresh();
    } catch (cause) {
      setError(cause);
    } finally {
      setBusy(false);
    }
  }
  let content;
  switch (tab) {
    case "agencies":
      content = (
        <AgenciesPage
          onEnter={enter}
          onPricing={(id) => {
            setPricingAgency(id);
            setTab("pricing");
          }}
        />
      );
      break;
    case "pricing":
      content = <PricingPage root={root} agencyId={pricingAgency} />;
      break;
    case "withdrawals":
      content = <WithdrawalsPage identity={identity} onAddAccount={() => setTab("accounts")} />;
      break;
    case "accounts":
      content = <AccountsPage identity={identity} />;
      break;
    case "exports":
      content = <ExportsPage key={`${identity.agency_id}:${exportKind}`} initialKind={exportKind} />;
      break;
    case "customers":
      content = <CustomersPage identity={identity} onEnter={enter} />;
      break;
    case "invitation":
      content = <InvitationsPage key={String(identity.agency_id)} />;
      break;
    case "ledger":
      content = (
        <LedgerPage
          onExport={() => {
            setExportKind("commissions");
            setTab("exports");
          }}
        />
      );
      break;
    case "sync":
      content = <SyncPage />;
      break;
    case "audit":
      content = <AuditPage root={root} />;
      break;
    case "security":
      content = <PasswordChange done={refresh} />;
      break;
    default:
      content = <OverviewPage identity={identity} />;
  }
  return (
    <main className="shell dashboard-shell">
      <header className="agency-header">
        <div className="brand-lockup">
          <div className="brand-mark" aria-hidden="true">N</div>
          <div>
            <p className="eyebrow">NEXIGHT · AGENCY HUB</p>
            <h1><span className="brand-name">NEXIGHT</span><span className="title-divider">/</span>{t("Agency Center")}<span className="product-attribution sr-only">New API</span></h1>
            <p className="muted header-subtitle">{root ? t("Root administrator") : identity.username}</p>
          </div>
        </div>
        <div className="actions">
          <select
            aria-label={t("Language")}
            value={i18n.language}
            onChange={(event) => void i18n.changeLanguage(event.target.value)}
          >
            <option value="zh">简体中文</option>
            <option value="zh-TW">繁體中文</option>
            <option value="en">English</option>
            <option value="fr">Français</option>
            <option value="ja">日本語</option>
            <option value="ru">Русский</option>
            <option value="vi">Tiếng Việt</option>
          </select>
          <button
            className="secondary button-icon"
            type="button"
            disabled={busy}
            onClick={() => {
              setBusy(true);
              void logout()
                .catch(setError)
                .finally(() => setBusy(false));
            }}
          >
            <ActionIcon name="close" />
            {t("Sign out")}
          </button>
        </div>
      </header>
      {root && identity.agency_id && (
        <div className="notice toolbar">
          <strong>{t("Managing agency {{id}}", { id: identity.agency_id })}</strong>
          <button className="secondary button-icon" type="button" disabled={busy} onClick={() => void leave()}>
            <ActionIcon name="close" />
            {t("Leave agency")}
          </button>
        </div>
      )}
      <ErrorNotice error={error} />
      <div className="dashboard-layout">
        <nav className="tabs dashboard-nav" aria-label={t("Agency navigation")}>
          <div className="dashboard-nav-heading">{t("Workspace")}</div>
          {tabs.map((item) => (
            <button
              className={tab === item ? "tab active button-icon" : "tab button-icon"}
              key={item}
              type="button"
              aria-current={tab === item ? "page" : undefined}
              onClick={() => {
                setTab(item);
                setError(null);
              }}
            >
              <span className="tab-icon" aria-hidden="true"><NavIcon tab={item} /></span>
              <span>{t(tabLabels[item])}</span>
            </button>
          ))}
        </nav>
        <section className="panel dashboard-panel dashboard-content" key={tab}>
          {content}
        </section>
      </div>
    </main>
  );
}
