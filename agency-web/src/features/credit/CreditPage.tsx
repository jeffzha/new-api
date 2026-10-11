import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { ActionIcon } from "../../components/Heading";
import { PageHeader } from "../../components/PageHeader";
import { DataTable, Dialog, ErrorNotice, Field, Loading, Time } from "../../components/ui";
import { api } from "../../lib/api";
import { useMutation } from "../../lib/mutation-context";
import { useQuery } from "../../lib/query";
import type { Identity } from "../../lib/types";
import {
  emailStatusKey,
  formatCreditYuan,
  quotaToYuanInput,
  statementStatusKey,
  yuanToQuota,
} from "./contracts";
import type { CreditAccount, CreditStatement } from "./contracts";

// Credit is the super administrator's platform-wide book: this page stays
// root-only, is never scoped by an acting agency, and never mixes credit with
// the customer's wallet balance.
export function CreditPage({ identity }: { identity: Identity }) {
  const { t } = useTranslation();
  const root = identity.actor_type === "root";
  const mutate = useMutation();
  const [query, setQuery] = useState("");
  const [account, setAccount] = useState<CreditAccount | null>(null);
  const [lookupBusy, setLookupBusy] = useState(false);
  const [lookupError, setLookupError] = useState<unknown>(null);
  const [notice, setNotice] = useState("");
  const [collect, setCollect] = useState<CreditStatement | null>(null);
  const [writeOff, setWriteOff] = useState<CreditStatement | null>(null);
  const [resendBusy, setResendBusy] = useState(0);
  const accounts = useQuery<{ items: CreditAccount[] }>(root ? "/root/credit/terms" : null);
  const statements = useQuery<{ items: CreditStatement[] }>(
    root && account
      ? `/root/credit-invoices?user_id=${encodeURIComponent(account.user_id)}`
      : null,
  );

  async function lookUp(event: React.FormEvent) {
    event.preventDefault();
    setNotice("");
    const value = query.trim();
    if (!value) {
      setLookupError(new Error("Enter a customer account or user ID first."));
      return;
    }
    setLookupBusy(true);
    setLookupError(null);
    try {
      const found = await api<CreditAccount>(
        `/root/credit/users/resolve?query=${encodeURIComponent(value)}`,
      );
      setAccount(found);
    } catch (cause) {
      setLookupError(cause);
    } finally {
      setLookupBusy(false);
    }
  }

  async function resend(statement: CreditStatement) {
    setResendBusy(statement.id);
    setLookupError(null);
    setNotice("");
    try {
      await mutate({
        path: `/root/credit-invoices/${statement.id}/resend-email`,
        body: {},
      });
      setNotice("Statement email sent");
      statements.reload();
    } catch (cause) {
      setLookupError(cause);
    } finally {
      setResendBusy(0);
    }
  }

  function reload() {
    accounts.reload();
    statements.reload();
  }

  if (!root) return null;
  return (
    <section>
      <PageHeader
        icon="credit"
        title={t("Credit & statements")}
        description={t(
          "Configure a credit line per customer, then track monthly statements, collections and bad debt.",
        )}
        actions={
          <button type="button" className="secondary button-icon" onClick={reload}>
            <ActionIcon name="refresh" />
            {t("Refresh")}
          </button>
        }
      />
      <p className="muted">
        {t(
          "Every statement is due on day 1 of the month; the payment term is the grace period that follows.",
        )}
      </p>
      <ErrorNotice error={accounts.error} />
      {notice && (
        <p className="success" role="status">
          {t(notice)}
        </p>
      )}
      <form className="toolbar" onSubmit={lookUp}>
        <Field label={t("Customer account or user ID")}>
          <input
            autoComplete="off"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder={t("Customer account or user ID")}
          />
        </Field>
        <button className="button-icon" type="submit" disabled={lookupBusy}>
          <ActionIcon name="search" />
          {t("Search")}
        </button>
      </form>
      <ErrorNotice error={lookupError} />
      {account && (
        <section>
          <h3>
            {account.username}
            {account.account_name ? ` · ${account.account_name}` : ""}
          </h3>
          <div className="metrics commission-metrics">
            <article>
              <span>{t("Credit limit")}</span>
              <strong>{formatCreditYuan(account.credit_limit)}</strong>
            </article>
            <article>
              <span>{t("Used credit")}</span>
              <strong>{formatCreditYuan(account.used_quota)}</strong>
            </article>
            <article>
              <span>{t("Available credit")}</span>
              <strong>{formatCreditYuan(account.available_quota)}</strong>
            </article>
            <article>
              <span>{t("Payment term (days)")}</span>
              <strong>{account.payment_term_days ?? 0}</strong>
            </article>
            <article>
              <span>{t("Repayment day")}</span>
              <strong>{account.repayment_day ?? 1}</strong>
            </article>
          </div>
          <CreditTermsForm
            account={account}
            onSaved={(saved) => {
              setAccount(saved);
              setNotice("Credit terms saved");
              reload();
            }}
          />
          <h3>{t("Credit statements")}</h3>
          <ErrorNotice error={statements.error} />
          {statements.loading ? (
            <Loading />
          ) : (
            <DataTable
              rows={statements.data?.items || []}
              rowKey={(row) => String(row.id)}
              empty={t("No records yet.")}
              columns={[
                { key: "period", label: "Statement period" },
                {
                  key: "amount_quota",
                  label: "Amount",
                  render: (row) => formatCreditYuan(row.amount_quota),
                },
                {
                  key: "remaining_quota",
                  label: "Remaining",
                  render: (row) => formatCreditYuan(row.remaining_quota),
                },
                {
                  key: "status",
                  label: "Statement status",
                  render: (row) => t(statementStatusKey(row.status)),
                },
                {
                  key: "due_at_ms",
                  label: "Due at",
                  render: (row) => <Time value={row.due_at_ms} />,
                },
                {
                  key: "email_status",
                  label: "Email status",
                  render: (row) => t(emailStatusKey(row.email_status)),
                },
              ]}
              actions={(row) => (
                <>
                  {(row.status === "issued" || row.status === "overdue") && (
                    <>
                      <button
                        type="button"
                        className="button-icon compact-action"
                        onClick={() => {
                          setNotice("");
                          setCollect(row);
                        }}
                      >
                        <ActionIcon name="check" />
                        {t("Mark as paid")}
                      </button>
                      <button
                        type="button"
                        className="secondary button-icon compact-action"
                        disabled={row.status !== "overdue"}
                        onClick={() => {
                          setNotice("");
                          setWriteOff(row);
                        }}
                      >
                        <ActionIcon name="ban" />
                        {t("Write off as bad debt")}
                      </button>
                      <button
                        type="button"
                        className="secondary button-icon compact-action"
                        disabled={resendBusy === row.id}
                        onClick={() => void resend(row)}
                      >
                        <ActionIcon name="play" />
                        {t("Resend email")}
                      </button>
                    </>
                  )}
                </>
              )}
            />
          )}
        </section>
      )}
      <h3>{t("Configured credit lines")}</h3>
      {accounts.loading ? (
        <Loading />
      ) : (
        <DataTable
          rows={accounts.data?.items || []}
          rowKey={(row) => row.user_id}
          empty={t("No credit terms yet. Look up a customer to set the first credit line.")}
          columns={[
            { key: "username", label: "Customer account" },
            {
              key: "credit_limit",
              label: "Credit limit",
              render: (row) => formatCreditYuan(row.credit_limit),
            },
            {
              key: "used_quota",
              label: "Used credit",
              render: (row) => formatCreditYuan(row.used_quota),
            },
            {
              key: "available_quota",
              label: "Available credit",
              render: (row) => formatCreditYuan(row.available_quota),
            },
            { key: "payment_term_days", label: "Payment term (days)" },
            {
              key: "status",
              label: "Status",
              render: (row) =>
                t(row.status === "suspended" ? "Credit suspended" : "Credit active"),
            },
          ]}
          actions={(row) => (
            <button
              type="button"
              className="secondary button-icon compact-action"
              onClick={() => {
                setQuery(row.username);
                setAccount(row);
                setNotice("");
                setLookupError(null);
              }}
            >
              <ActionIcon name="eye" />
              {t("Credit statements")}
            </button>
          )}
        />
      )}
      {collect && (
        <CollectDialog
          statement={collect}
          onClose={() => setCollect(null)}
          onSaved={() => {
            setCollect(null);
            setNotice("Statement settled");
            reload();
          }}
        />
      )}
      {writeOff && (
        <WriteOffDialog
          statement={writeOff}
          onClose={() => setWriteOff(null)}
          onSaved={() => {
            setWriteOff(null);
            setNotice("Statement written off as bad debt");
            reload();
          }}
        />
      )}
    </section>
  );
}

function CreditTermsForm(props: {
  account: CreditAccount;
  onSaved: (account: CreditAccount) => void;
}) {
  const { t } = useTranslation();
  const mutate = useMutation();
  const [limit, setLimit] = useState(quotaToYuanInput(props.account.credit_limit));
  const [termDays, setTermDays] = useState(String(props.account.payment_term_days ?? 0));
  const [emailEnabled, setEmailEnabled] = useState(Boolean(props.account.email_enabled));
  const [status, setStatus] = useState(
    props.account.configured && props.account.status === "suspended" ? "suspended" : "active",
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    setLimit(quotaToYuanInput(props.account.credit_limit));
    setTermDays(String(props.account.payment_term_days ?? 0));
    setEmailEnabled(Boolean(props.account.email_enabled));
    setStatus(
      props.account.configured && props.account.status === "suspended" ? "suspended" : "active",
    );
    setError(null);
  }, [props.account]);

  async function save(event: React.FormEvent) {
    event.preventDefault();
    const creditLimit = yuanToQuota(limit);
    if (creditLimit === null) {
      setError(new Error("Enter a valid credit limit in CNY."));
      return;
    }
    const paymentTermDays = Number(termDays);
    if (!Number.isInteger(paymentTermDays) || paymentTermDays < 0 || paymentTermDays > 365) {
      setError(new Error("Enter a valid payment term in days."));
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await mutate({
        path: `/root/users/${props.account.user_id}/credit`,
        method: "PUT",
        body: {
          credit_limit: creditLimit,
          payment_term_days: paymentTermDays,
          status,
          email_enabled: emailEnabled,
        },
      });
      props.onSaved({
        ...props.account,
        configured: true,
        credit_limit: creditLimit,
        available_quota: status === "active" ? Math.max(0, creditLimit - props.account.used_quota) : 0,
        payment_term_days: paymentTermDays,
        email_enabled: emailEnabled,
        status,
      });
    } catch (cause) {
      setError(cause);
    } finally {
      setBusy(false);
    }
  }

  return (
    <form onSubmit={save}>
      <fieldset disabled={busy}>
        <div className="credential-grid">
          <Field label={t("Credit limit (CNY)")}>
            <input
              required
              inputMode="decimal"
              value={limit}
              onChange={(event) => setLimit(event.target.value)}
            />
          </Field>
          <Field label={t("Payment term (days)")}>
            <input
              required
              type="number"
              min={0}
              max={365}
              value={termDays}
              onChange={(event) => setTermDays(event.target.value)}
            />
          </Field>
          <Field label={t("Status")}>
            <select value={status} onChange={(event) => setStatus(event.target.value)}>
              <option value="active">{t("Credit active")}</option>
              <option value="suspended">{t("Credit suspended")}</option>
            </select>
          </Field>
          <Field label={t("Send statement emails")}>
            <input
              type="checkbox"
              checked={emailEnabled}
              onChange={(event) => setEmailEnabled(event.target.checked)}
            />
          </Field>
        </div>
        <ErrorNotice error={error} />
        <button className="button-icon" type="submit">
          <ActionIcon name="save" />
          {t("Save credit terms")}
        </button>
      </fieldset>
    </form>
  );
}

function CollectDialog(props: {
  statement: CreditStatement;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { t } = useTranslation();
  const mutate = useMutation();
  const [amount, setAmount] = useState("");
  const [reference, setReference] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);

  async function confirm(event: React.FormEvent) {
    event.preventDefault();
    if (!reference.trim()) {
      setError(new Error("Payment reference is required."));
      return;
    }
    let amountQuota = 0;
    if (amount.trim() !== "") {
      const parsed = yuanToQuota(amount);
      if (parsed === null) {
        setError(new Error("Enter a valid amount in CNY."));
        return;
      }
      amountQuota = parsed;
    }
    setBusy(true);
    setError(null);
    try {
      await mutate({
        path: `/root/credit-invoices/${props.statement.id}/mark-paid`,
        body: { amount_quota: amountQuota, payment_reference: reference.trim() },
      });
      props.onSaved();
    } catch (cause) {
      setError(cause);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog title={t("Register collection")} onClose={props.onClose} busy={busy}>
      <p>
        {props.statement.username} · {props.statement.period} ·{" "}
        {formatCreditYuan(props.statement.remaining_quota)}
      </p>
      <form onSubmit={confirm}>
        <fieldset disabled={busy}>
          <Field label={t("Collection amount (CNY)")} hint={t("Leave blank to settle the remaining balance.")}>
            <input
              inputMode="decimal"
              value={amount}
              onChange={(event) => setAmount(event.target.value)}
            />
          </Field>
          <Field label={t("Payment reference")}>
            <input
              required
              maxLength={191}
              autoComplete="off"
              value={reference}
              onChange={(event) => setReference(event.target.value)}
            />
          </Field>
          <ErrorNotice error={error} />
          <button className="button-icon" type="submit">
            <ActionIcon name="check" />
            {t("Confirm collection")}
          </button>
        </fieldset>
      </form>
    </Dialog>
  );
}

function WriteOffDialog(props: {
  statement: CreditStatement;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { t } = useTranslation();
  const mutate = useMutation();
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);

  async function confirm(event: React.FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await mutate({
        path: `/root/credit-invoices/${props.statement.id}/mark-bad-debt`,
        body: { reason: reason.trim() },
      });
      props.onSaved();
    } catch (cause) {
      setError(cause);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog title={t("Write off as bad debt")} onClose={props.onClose} busy={busy}>
      <p>
        {t(
          "Writing off suspends the credit line and keeps the delivered usage as historical evidence.",
        )}
      </p>
      <p>
        {props.statement.username} · {props.statement.period} ·{" "}
        {formatCreditYuan(props.statement.remaining_quota)}
      </p>
      <form onSubmit={confirm}>
        <fieldset disabled={busy}>
          <Field label={t("Bad debt reason")}>
            <textarea
              required
              rows={3}
              maxLength={512}
              value={reason}
              onChange={(event) => setReason(event.target.value)}
            />
          </Field>
          <ErrorNotice error={error} />
          <button className="danger" type="submit">
            {t("Confirm write-off")}
          </button>
        </fieldset>
      </form>
    </Dialog>
  );
}
