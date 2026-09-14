import { useState } from "react";
import { useTranslation } from "react-i18next";
import { DataTable, Dialog, ErrorNotice, Field, Loading, formatMoney } from "../../components/ui";
import { useMutation } from "../../lib/mutation-context";
import { useQuery } from "../../lib/query";
import {
  actionLabels,
  createWithdrawalRequest,
  paidRequest,
  statusLabels,
  transitionRequest,
} from "./contracts";
import type {
  CommissionBalance,
  PaymentLease,
  PayoutAccount,
  UnpaidEvidence,
  Withdrawal,
  WithdrawalAction,
} from "./contracts";

export function WithdrawalForm(props: {
  agencyID: number;
  onClose: () => void;
  onSaved: () => void;
  onAddAccount: () => void;
}) {
  const { t } = useTranslation();
  const mutate = useMutation();
  const accounts = useQuery<{ items: PayoutAccount[] }>("/withdrawal-accounts");
  const balances = useQuery<{ items: CommissionBalance[] }>("/commissions/summary");
  const [accountID, setAccountID] = useState("");
  const currency = "CNY";
  const [amount, setAmount] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>();
  const account = accounts.data?.items.find((row) => String(row.id) === accountID);
  const balance = balances.data?.items.find((row) => row.currency_code === currency);
  async function submit(event: React.FormEvent) {
    event.preventDefault();
    setError(undefined);
    if (!account || !balance) {
      setError(t("Select a payout account and currency."));
      return;
    }
    setBusy(true);
    try {
      await mutate(createWithdrawalRequest(props.agencyID, account, amount, balance));
      props.onSaved();
    } catch (cause) {
      setError(cause instanceof Error ? t(cause.message) : cause);
    } finally {
      setBusy(false);
    }
  }
  return (
    <Dialog title={t("Request withdrawal")} onClose={props.onClose} busy={busy}>
      <p>
        {t(
          "Submitting locks this amount from your available commission. Payment is reviewed and processed manually.",
        )}
      </p>
      <ErrorNotice error={accounts.error || balances.error} />
      {accounts.loading || balances.loading ? (
        <Loading />
      ) : (
        <form onSubmit={submit}>
          <fieldset disabled={busy}>
            <DataTable
              rows={balances.data?.items || []}
              rowKey={(row) => row.currency_code}
              empty={t(
                "No commission balance yet. Review the commission ledger before withdrawing.",
              )}
              columns={[
                { key: "currency_code", label: "Currency" },
                {
                  key: "available_micros",
                  label: "Available commission",
                  render: (row) => formatMoney(row.available_micros, row.currency_code),
                },
                {
                  key: "locked_micros",
                  label: "Locked commission",
                  render: (row) => formatMoney(row.locked_micros, row.currency_code),
                },
              ]}
            />
            {!accounts.data?.items.length && (
              <div className="actions">
                <p>{t("Add a payout account before requesting a withdrawal.")}</p>
                <button type="button" className="secondary" onClick={props.onAddAccount}>
                  {t("Add payout account")}
                </button>
              </div>
            )}
            {accounts.data?.items.length ? (
              <button type="button" className="secondary" onClick={props.onAddAccount}>
                {t("Add payout account")}
              </button>
            ) : null}
            <Field label={t("Payout account")}>
              <select
                required
                value={accountID}
                onChange={(event) => setAccountID(event.target.value)}
              >
                <option value="">{t("Choose an account")}</option>
                {accounts.data?.items.map((row) => (
                  <option key={row.id} value={String(row.id)}>
                    •••• {row.last4} · {t("Version")} {row.version}
                  </option>
                ))}
              </select>
            </Field>
            <Field label={t("Currency")}>
              <output>{t("Chinese yuan (CNY)")}</output>
            </Field>
            <Field
              label={t("Withdrawal amount")}
              hint={t("Enter the currency amount, for example 100.00.")}
            >
              <input
                required
                inputMode="decimal"
                autoComplete="off"
                maxLength={24}
                value={amount}
                onChange={(event) => setAmount(event.target.value)}
              />
            </Field>
            <ErrorNotice error={error} />
            <button type="submit" disabled={!account || !balance}>
              {t("Confirm withdrawal request")}
            </button>
          </fieldset>
        </form>
      )}
    </Dialog>
  );
}

export function WithdrawalActionDialog(props: {
  row: Withdrawal;
  action: WithdrawalAction;
  isRoot: boolean;
  lease?: PaymentLease;
  onClose: () => void;
  onSaved: (lease?: PaymentLease) => void;
}) {
  const { t } = useTranslation();
  const mutate = useMutation();
  const [reason, setReason] = useState("");
  const [channel, setChannel] = useState("bank");
  const [reference, setReference] = useState("");
  const [bankConfirmation, setBankConfirmation] = useState("");
  const [confirmedAt, setConfirmedAt] = useState("");
  const [confirmed, setConfirmed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>();
  const recovery = props.action === "recover_unpaid";
  const paymentAction = recovery || ["paid", "paying", "payment_unknown"].includes(props.action);
  async function submit(event: React.FormEvent) {
    event.preventDefault();
    setError(undefined);
    setBusy(true);
    try {
      if (paymentAction && !confirmed)
        throw new Error("Confirm that the original payment has been checked.");
      const unpaidEvidence: UnpaidEvidence | undefined = recovery
        ? {
            outcome: "not_paid",
            payment_channel: channel.trim(),
            original_payment_reference: reference.trim(),
            bank_confirmation_reference: bankConfirmation.trim(),
            confirmed_at: new Date(confirmedAt).toISOString(),
          }
        : undefined;
      const request =
        props.action === "paid"
          ? paidRequest(props.row, channel, reference, props.lease)
          : transitionRequest(props.row, props.action, props.isRoot, reason, unpaidEvidence);
      const result = await mutate<Partial<PaymentLease>>(request);
      let lease: PaymentLease | undefined;
      if (
        props.action === "paying" &&
        typeof result.payment_lease_token === "string" &&
        result.payment_lease_until !== undefined
      ) {
        lease = {
          payment_lease_token: result.payment_lease_token,
          payment_lease_until: result.payment_lease_until,
        };
      }
      props.onSaved(lease);
    } catch (cause) {
      setError(cause instanceof Error ? t(cause.message) : cause);
    } finally {
      setBusy(false);
    }
  }
  return (
    <Dialog title={t(actionLabels[props.action])} onClose={props.onClose} busy={busy}>
      <dl className="details">
        <dt>{t("Withdrawal")}</dt>
        <dd>{props.row.request_no}</dd>
        <dt>{t("Amount")}</dt>
        <dd>{formatMoney(props.row.amount_micros, props.row.currency_code)}</dd>
        <dt>{t("Current status")}</dt>
        <dd>{t(statusLabels[props.row.status] || props.row.status)}</dd>
        <dt>{t("Version")}</dt>
        <dd>{props.row.version}</dd>
      </dl>
      {props.action === "paying" && (
        <p>
          {t(
            "This only records the start of a manual payment and reserves a ten-minute processing lease. It does not transfer money.",
          )}
        </p>
      )}
      {props.action === "paid" && (
        <p>
          {t(
            "Record only a confirmed successful payment. Use the original bank reference; do not make another transfer.",
          )}
        </p>
      )}
      {props.action === "payment_unknown" && (
        <p>
          {t("Funds remain locked until the original bank payment is verified. Do not pay again.")}
        </p>
      )}
      {recovery && (
        <p>
          {t(
            "Restore approval only after the bank confirms that the original transfer was not paid. Funds stay locked; starting any later payment requires a separate review.",
          )}
        </p>
      )}
      {(props.action === "cancelled" || props.action === "rejected") && (
        <p>
          {t(
            "This releases the locked commission back to the agency. Confirm that no external payment occurred.",
          )}
        </p>
      )}
      {props.action === "on_hold" && (
        <p>{t("This pauses processing and keeps the commission locked for investigation.")}</p>
      )}
      <form onSubmit={submit}>
        <fieldset disabled={busy}>
          {(props.action === "paid" || recovery) && (
            <>
              <Field label={t("Payment channel")}>
                <input
                  required
                  maxLength={64}
                  value={channel}
                  onChange={(event) => setChannel(event.target.value)}
                />
              </Field>
              <Field label={t("Original bank reference")}>
                <input
                  required
                  maxLength={191}
                  autoComplete="off"
                  value={reference}
                  onChange={(event) => setReference(event.target.value)}
                />
              </Field>
            </>
          )}
          {recovery && (
            <>
              <Field
                label={t("Bank investigation reference")}
                hint={t(
                  "Use the bank case number or confirmation document reference for the original transfer.",
                )}
              >
                <input
                  required
                  maxLength={191}
                  autoComplete="off"
                  value={bankConfirmation}
                  onChange={(event) => setBankConfirmation(event.target.value)}
                />
              </Field>
              <Field
                label={t("Bank confirmation time")}
                hint={t(
                  "Enter the local time when the bank confirmed the original payment was not made.",
                )}
              >
                <input
                  required
                  type="datetime-local"
                  step="0.001"
                  value={confirmedAt}
                  onChange={(event) => setConfirmedAt(event.target.value)}
                />
              </Field>
            </>
          )}
          {props.action !== "paid" && (
            <Field label={t("Reason and evidence")}>
              <textarea
                required
                maxLength={1000}
                rows={4}
                value={reason}
                onChange={(event) => setReason(event.target.value)}
              />
            </Field>
          )}
          {paymentAction && (
            <label className="check-field">
              <input
                type="checkbox"
                required
                checked={confirmed}
                onChange={(event) => setConfirmed(event.target.checked)}
              />
              <span>
                {t(
                  recovery
                    ? "The bank confirmed that the original payment was not made; the evidence above refers to that attempt."
                    : "I checked the original payment and will not make a duplicate transfer.",
                )}
              </span>
            </label>
          )}
          <ErrorNotice error={error} />
          <button type="submit" disabled={paymentAction && !confirmed}>
            {t(actionLabels[props.action])}
          </button>
        </fieldset>
      </form>
    </Dialog>
  );
}
