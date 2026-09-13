import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { DataTable, Dialog, ErrorNotice, Field, Loading, formatTime } from "../../components/ui";
import { useMutation } from "../../lib/mutation-context";
import { useQuery } from "../../lib/query";
import type { Identity } from "../../lib/types";
import { accountMutation, currentVersion } from "./contracts";
import type { AccountInput, PayoutAccount, RevealedAccount } from "./contracts";

export function AccountsPage(props: { identity: Identity }) {
  const { t } = useTranslation();
  const query = useQuery<{ items: PayoutAccount[] }>(
    props.identity.agency_id ? "/withdrawal-accounts" : null,
  );
  const [edit, setEdit] = useState<PayoutAccount | "new" | null>(null);
  const [disable, setDisable] = useState<PayoutAccount | null>(null);
  const [reveal, setReveal] = useState<PayoutAccount | null>(null);
  if (!props.identity.agency_id)
    return <p className="empty">{t("Enter an agency to manage its payout accounts.")}</p>;
  return (
    <section>
      <div className="toolbar">
        <h2>{t("Payout accounts")}</h2>
        <div className="actions">
          <button type="button" className="secondary" onClick={query.reload}>
            {t("Refresh")}
          </button>
          <button type="button" onClick={() => setEdit("new")}>
            {t("Add payout account")}
          </button>
        </div>
      </div>
      <p className="muted">
        {t(
          "Only the latest account is active. Changes create a new version and leave existing withdrawal snapshots unchanged.",
        )}
      </p>
      <ErrorNotice error={query.error} />
      {query.loading ? (
        <Loading />
      ) : (
        <DataTable
          rows={query.data?.items || []}
          rowKey={(row) => String(row.id)}
          empty={t("Add a payout account before requesting a withdrawal.")}
          columns={[
            { key: "id", label: "Account ID" },
            { key: "last4", label: "Account ending", render: (row) => `•••• ${row.last4}` },
            { key: "version", label: "Version" },
            {
              key: "created_at_ms",
              label: "Created",
              render: (row) => formatTime(row.created_at_ms),
            },
            {
              key: "actions",
              label: "Actions",
              render: (row) => (
                <div className="actions">
                  <button className="secondary" type="button" onClick={() => setEdit(row)}>
                    {t("Replace account details")}
                  </button>
                  <button className="danger" type="button" onClick={() => setDisable(row)}>
                    {t("Disable account")}
                  </button>
                  {props.identity.actor_type === "root" && (
                    <button className="secondary" type="button" onClick={() => setReveal(row)}>
                      {t("Reveal account")}
                    </button>
                  )}
                </div>
              ),
            },
          ]}
        />
      )}
      {edit && (
        <AccountEditor
          account={edit === "new" ? undefined : edit}
          agencyID={props.identity.agency_id}
          onClose={() => setEdit(null)}
          onSaved={() => {
            setEdit(null);
            query.reload();
          }}
        />
      )}
      {disable && (
        <DisableAccount
          account={disable}
          onClose={() => setDisable(null)}
          onSaved={() => {
            setDisable(null);
            query.reload();
          }}
        />
      )}
      {reveal && <RevealAccount account={reveal} onClose={() => setReveal(null)} />}
    </section>
  );
}

function AccountEditor(props: {
  account?: PayoutAccount;
  agencyID: number;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { t } = useTranslation();
  const mutate = useMutation();
  const [input, setInput] = useState<AccountInput>({
    account_type: "bank",
    account_name: "",
    account_no: "",
    bank_name: "",
  });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>();
  async function save(event: React.FormEvent) {
    event.preventDefault();
    setError(undefined);
    setBusy(true);
    try {
      await mutate(accountMutation(props.agencyID, input, props.account));
      props.onSaved();
    } catch (cause) {
      setError(cause instanceof Error ? t(cause.message) : cause);
    } finally {
      setBusy(false);
    }
  }
  return (
    <Dialog
      title={t(props.account ? "Replace account details" : "Add payout account")}
      busy={busy}
      onClose={props.onClose}
    >
      <p>
        {t(
          "Enter the complete new account details. Existing withdrawals retain their original recipient.",
        )}
      </p>
      <form onSubmit={save}>
        <fieldset disabled={busy}>
          <Field label={t("Account type")}>
            <select
              value={input.account_type}
              onChange={(event) => setInput({ ...input, account_type: event.target.value })}
            >
              <option value="bank">{t("Bank account")}</option>
              <option value="corporate">{t("Corporate bank account")}</option>
            </select>
          </Field>
          <Field label={t("Account holder")}>
            <input
              required
              autoComplete="off"
              maxLength={128}
              value={input.account_name}
              onChange={(event) => setInput({ ...input, account_name: event.target.value })}
            />
          </Field>
          <Field label={t("Account number")}>
            <input
              required
              autoComplete="off"
              minLength={4}
              maxLength={128}
              value={input.account_no}
              onChange={(event) => setInput({ ...input, account_no: event.target.value })}
            />
          </Field>
          <Field label={t("Bank and branch")}>
            <input
              required
              autoComplete="off"
              maxLength={256}
              value={input.bank_name}
              onChange={(event) => setInput({ ...input, bank_name: event.target.value })}
            />
          </Field>
          <ErrorNotice error={error} />
          <button type="submit">{t("Save account version")}</button>
        </fieldset>
      </form>
    </Dialog>
  );
}

function DisableAccount(props: {
  account: PayoutAccount;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { t } = useTranslation();
  const mutate = useMutation();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>();
  async function confirm() {
    setBusy(true);
    setError(undefined);
    try {
      await mutate({
        path: `/withdrawal-accounts/${props.account.id}/disable`,
        action: "withdrawal_account.disable",
        objectId: `withdrawal_account:${props.account.id}`,
        body: { expected_version: currentVersion(props.account.version) },
      });
      props.onSaved();
    } catch (cause) {
      setError(cause instanceof Error ? t(cause.message) : cause);
    } finally {
      setBusy(false);
    }
  }
  return (
    <Dialog title={t("Disable account")} onClose={props.onClose} busy={busy}>
      <p>
        {t(
          "This account will no longer accept new withdrawal requests. Existing withdrawal snapshots remain unchanged.",
        )}
      </p>
      <p>
        •••• {props.account.last4} · {t("Version")} {props.account.version}
      </p>
      <ErrorNotice error={error} />
      <button className="danger" type="button" disabled={busy} onClick={() => void confirm()}>
        {t("Confirm disable")}
      </button>
    </Dialog>
  );
}

export function RevealAccount(props: {
  account: Pick<PayoutAccount, "id" | "version">;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const mutate = useMutation();
  const [data, setData] = useState<RevealedAccount>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>();
  useEffect(() => {
    if (!data) return;
    const timeout = window.setTimeout(() => setData(undefined), 60000);
    const conceal = () => {
      if (document.visibilityState === "hidden") setData(undefined);
    };
    document.addEventListener("visibilitychange", conceal);
    return () => {
      window.clearTimeout(timeout);
      document.removeEventListener("visibilitychange", conceal);
    };
  }, [data]);
  async function reveal() {
    setBusy(true);
    setError(undefined);
    try {
      const result = await mutate<RevealedAccount>({
        path: `/root/withdrawal-accounts/${props.account.id}/reveal`,
        action: "withdrawal_account.reveal",
        objectId: `withdrawal_account:${props.account.id}`,
        body: {},
      });
      if (String(result.version) !== String(props.account.version))
        throw new Error("The account version does not match the withdrawal snapshot.");
      setData(result);
    } catch (cause) {
      setError(cause instanceof Error ? t(cause.message) : cause);
    } finally {
      setBusy(false);
    }
  }
  return (
    <Dialog title={t("Reveal account")} onClose={props.onClose} busy={busy}>
      <p>
        {t("Account details are audited and hidden after one minute or when this tab is hidden.")}
      </p>
      <ErrorNotice error={error} />
      {data ? (
        <>
          <dl className="details">
            <dt>{t("Account holder")}</dt>
            <dd>{data.account_name}</dd>
            <dt>{t("Account number")}</dt>
            <dd>{data.account_no}</dd>
            <dt>{t("Bank and branch")}</dt>
            <dd>{data.bank_name}</dd>
            <dt>{t("Version")}</dt>
            <dd>{data.version}</dd>
          </dl>
          <button className="secondary" type="button" onClick={() => setData(undefined)}>
            {t("Hide account")}
          </button>
        </>
      ) : (
        <button type="button" disabled={busy} onClick={() => void reveal()}>
          {t("Verify and reveal")}
        </button>
      )}
    </Dialog>
  );
}
