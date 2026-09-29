import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { api, hubConfig } from "../lib/api";
import { startPlatformSSO } from "../lib/platform";
import { ActionIcon } from "./Heading";
import { ErrorNotice, Field } from "./ui";

export function Login({ done }: { done: () => Promise<void> }) {
  const { t } = useTranslation();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const controller = useRef<AbortController | null>(null);
  useEffect(() => () => controller.current?.abort(), []);
  async function signIn(platform: boolean) {
    if (busy) return;
    setBusy(true);
    setError(null);
    const abort = new AbortController();
    controller.current = abort;
    try {
      if (platform) await startPlatformSSO(abort.signal);
      else {
        const { nonce } = await api<{ nonce: string }>("/auth/nonce", { signal: abort.signal });
        await api("/auth/login", {
          method: "POST",
          body: JSON.stringify({ username, password, nonce }),
          signal: abort.signal,
        });
      }
      setPassword("");
      await done();
    } catch (cause) {
      if (!abort.signal.aborted) setError(cause);
    } finally {
      controller.current = null;
      if (!abort.signal.aborted) setBusy(false);
    }
  }
  return (
    <main className="shell">
      <section className="auth">
        <p className="eyebrow">NEXIGHT · AGENCY HUB</p>
        <h1><span className="brand-name">NEXIGHT</span><span className="title-divider">/</span>{t("Agency Center")}<span className="product-attribution sr-only">New API</span></h1>
        {hubConfig().platform_base_url && (
          <button
            type="button"
            className="secondary sso"
            disabled={busy}
            onClick={() => void signIn(true)}
          >
            {t("Continue with platform session")}
          </button>
        )}
        <form
          onSubmit={(event) => {
            event.preventDefault();
            void signIn(false);
          }}
        >
          <Field label={t("Username")}>
            <input
              autoComplete="username"
              value={username}
              onChange={(event) => setUsername(event.target.value)}
              disabled={busy}
              required
            />
          </Field>
          <Field label={t("Password")}>
            <input
              type="password"
              autoComplete="current-password"
              value={password}
              onChange={(event) => setPassword(event.target.value)}
              disabled={busy}
              required
            />
          </Field>
          <ErrorNotice error={error} />
          <button disabled={busy}>{t(busy ? "Signing in…" : "Sign in")}</button>
        </form>
      </section>
    </main>
  );
}

export function PasswordChange({
  done,
  required = false,
  onSignOut,
}: {
  done: () => Promise<void>;
  required?: boolean;
  onSignOut?: () => Promise<void>;
}) {
  const { t } = useTranslation();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [signingOut, setSigningOut] = useState(false);
  const [saved, setSaved] = useState(false);
  async function submit() {
    if (busy) return;
    setError(null);
    setSaved(false);
    const length = new TextEncoder().encode(next).length;
    if (next !== confirm) {
      setError(new Error("The passwords do not match."));
      return;
    }
    if (length < 8 || length > 20) {
      setError(new Error("Use a password between 8 and 20 bytes."));
      return;
    }
    setBusy(true);
    try {
      await api("/auth/change-password", {
        method: "POST",
        body: JSON.stringify({ current_password: current, new_password: next }),
      });
      setCurrent("");
      setNext("");
      setConfirm("");
      setSaved(true);
      await done();
    } catch (cause) {
      setError(cause);
    } finally {
      setBusy(false);
    }
  }
  async function signOut() {
    if (!onSignOut || busy || signingOut) return;
    setError(null);
    setSigningOut(true);
    try {
      await onSignOut();
    } catch (cause) {
      setError(cause);
      setSigningOut(false);
    }
  }
  return (
    <section className={`password-change${required ? " password-change-required" : ""}`}>
      <div className="password-change-heading">
        {required && <p className="eyebrow">NEXIGHT · AGENCY HUB</p>}
        <h2>{t("Account security")}</h2>
      </div>
      {required && (
        <div className="notice password-change-notice">
          <span className="password-change-notice-icon" aria-hidden="true">
            <ActionIcon name="check" />
          </span>
          <p>{t("Change your temporary password before continuing.")}</p>
        </div>
      )}
      <form
        onSubmit={(event) => {
          event.preventDefault();
          void submit();
        }}
      >
        <Field label={t("Current password")}>
          <input
            type="password"
            autoComplete="current-password"
            value={current}
            onChange={(event) => setCurrent(event.target.value)}
            disabled={busy || signingOut}
            required
          />
        </Field>
        <Field label={t("New password")} hint={t("Use a password between 8 and 20 bytes.")}>
          <input
            type="password"
            autoComplete="new-password"
            value={next}
            onChange={(event) => setNext(event.target.value)}
            disabled={busy || signingOut}
            required
          />
        </Field>
        <Field label={t("Confirm password")}>
          <input
            type="password"
            autoComplete="new-password"
            value={confirm}
            onChange={(event) => setConfirm(event.target.value)}
            disabled={busy || signingOut}
            required
          />
        </Field>
        <ErrorNotice error={error} />
        {saved && <p role="status">{t("Password updated.")}</p>}
        <div className={`password-actions${onSignOut ? "" : " password-actions-single"}`}>
          {onSignOut && (
            <button
              className="secondary button-icon"
              type="button"
              disabled={busy || signingOut}
              onClick={() => void signOut()}
            >
              <ActionIcon name="close" />
              {t("Sign out")}
            </button>
          )}
          <button className="button-icon" disabled={busy || signingOut}>
            <ActionIcon name="save" />
            {t("Save password")}
          </button>
        </div>
      </form>
    </section>
  );
}
