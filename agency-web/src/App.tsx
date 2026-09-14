import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
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
          <button className="secondary" type="button" onClick={() => void logout().catch(setError)}>
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
    <main className="shell">
      <header>
        <div>
          <p className="eyebrow">AGENCY HUB</p>
          <h1>{t("Agency Center")}</h1>
          <p className="muted">{root ? t("Root administrator") : identity.username}</p>
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
            type="button"
            className="secondary"
            disabled={busy}
            onClick={() => {
              setBusy(true);
              void logout()
                .catch(setError)
                .finally(() => setBusy(false));
            }}
          >
            {t("Sign out")}
          </button>
        </div>
      </header>
      {root && identity.agency_id && (
        <div className="notice toolbar">
          <strong>{t("Managing agency {{id}}", { id: identity.agency_id })}</strong>
          <button className="secondary" type="button" disabled={busy} onClick={() => void leave()}>
            {t("Leave agency")}
          </button>
        </div>
      )}
      <nav className="tabs" aria-label={t("Agency navigation")}>
        {tabs.map((item) => (
          <button
            key={item}
            type="button"
            className={tab === item ? "tab active" : "tab"}
            aria-current={tab === item ? "page" : undefined}
            onClick={() => {
              setTab(item);
              setError(null);
            }}
          >
            {t(tabLabels[item])}
          </button>
        ))}
      </nav>
      <ErrorNotice error={error} />
      <section className="panel" key={tab}>
        {content}
      </section>
    </main>
  );
}
