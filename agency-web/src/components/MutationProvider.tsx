import { useCallback, useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { api, ApiError, bodyHash, canonicalBody } from "../lib/api";
import { requestRootProof } from "../lib/platform";
import { MutationContext } from "../lib/mutation-context";
import type { Identity, MutationRequest, Mutate } from "../lib/types";
import { Dialog, ErrorNotice, Field } from "./ui";

interface Pending {
  request: MutationRequest;
  key: string;
  resolve: (value: unknown) => void;
  reject: (reason: Error) => void;
}

function verificationScope(
  request: MutationRequest,
  identity: Identity,
): [string, string] | undefined {
  if (request.action) return [request.action, request.objectId || ""];
  const path = request.path;
  const delivery = path.match(/^\/root\/deliveries\/([^/]+)\/ack$/);
  if (delivery) return ["delivery.ack", "delivery:" + delivery[1]];
  if (path === "/root/reconciliation/runs") return ["reconciliation.run", "reconciliation:run"];
  if (path === "/withdrawals") return ["withdrawal.create", "agency:" + identity.agency_id];
  if (path === "/withdrawal-accounts")
    return ["withdrawal_account.create", "agency:" + identity.agency_id];
  return undefined;
}

export function MutationProvider(props: { identity: Identity; children: ReactNode }) {
  const { t } = useTranslation();
  const [pending, setPending] = useState<Pending | null>(null);
  const [password, setPassword] = useState("");
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const active = useRef<Pending | null>(null);
  const operationKeys = useRef(new Map<string, string>());
  const controller = useRef<AbortController | null>(null);
  useEffect(
    () => () => {
      controller.current?.abort();
      active.current?.reject(new Error("Operation cancelled."));
    },
    [],
  );

  const execute = useCallback(async (request: MutationRequest, key: string, proof?: string) => {
    const headers: Record<string, string> = { "Idempotency-Key": key };
    if (proof) headers["X-Agency-Verification-Proof"] = proof;
    const data = await api<unknown>(request.path, {
      method: request.method || "POST",
      body: canonicalBody(request.body),
      headers,
    });
    if (
      data &&
      typeof data === "object" &&
      "operation_id" in data &&
      "status" in data &&
      data.status === "processing"
    ) {
      throw new ApiError(
        "The operation is still processing. Retry with the same details.",
        202,
        "processing",
      );
    }
    return data;
  }, []);

  const mutate: Mutate = useCallback(
    async <T,>(request: MutationRequest): Promise<T> => {
      if (active.current) throw new Error("Complete the current confirmation first.");
      const signature = `${props.identity.actor_id}:${props.identity.agency_id}:${request.method || "POST"}:${request.path}:${canonicalBody(request.body)}`;
      const key = operationKeys.current.get(signature) || crypto.randomUUID();
      operationKeys.current.set(signature, key);
      const scope = verificationScope(request, props.identity);
      try {
        const result = scope
          ? await new Promise<unknown>((resolve, reject) => {
              const operation = {
                request: { ...request, action: scope[0], objectId: scope[1] },
                key,
                resolve,
                reject,
              };
              active.current = operation;
              setPending(operation);
              setPassword("");
              setError(null);
            })
          : await execute(request, key);
        operationKeys.current.delete(signature);
        return result as T;
      } catch (cause) {
        // A network error may occur after the server committed the operation.
        // Retain that key in memory so an explicit retry cannot submit it twice.
        if (cause instanceof ApiError && cause.status >= 400 && cause.status < 500)
          operationKeys.current.delete(signature);
        throw cause;
      }
    },
    [props.identity.actor_id, props.identity.agency_id, execute],
  );

  function cancel() {
    if (busy) return;
    active.current?.reject(new Error("Operation cancelled."));
    active.current = null;
    setPending(null);
    setPassword("");
  }
  async function confirm() {
    if (!pending || busy) return;
    setBusy(true);
    setError(null);
    const abort = new AbortController();
    controller.current = abort;
    try {
      const payload = {
        password,
        action: pending.request.action || "",
        object_id: pending.request.objectId || "",
        body_hash: await bodyHash(pending.request.body),
      };
      const proof =
        props.identity.actor_type === "root"
          ? await requestRootProof(payload, abort.signal)
          : (
              await api<{ proof: string }>("/auth/verify", {
                method: "POST",
                body: JSON.stringify(payload),
                signal: abort.signal,
              })
            ).proof;
      const result = await execute(pending.request, pending.key, proof);
      pending.resolve(result);
      active.current = null;
      setPending(null);
      setPassword("");
    } catch (cause) {
      setError(cause);
      if (
        cause instanceof ApiError &&
        cause.status >= 400 &&
        cause.status < 500 &&
        cause.code !== "invalid_credentials" &&
        cause.code !== "invalid_verification"
      ) {
        pending.reject(cause);
        active.current = null;
        setPending(null);
      }
    } finally {
      setBusy(false);
      controller.current = null;
    }
  }
  return (
    <MutationContext.Provider value={mutate}>
      {props.children}
      {pending && (
        <Dialog title={t("Confirm this action")} onClose={cancel} busy={busy}>
          <p>
            {t(
              "Confirm the details on this page before continuing. This action will be recorded in the audit log.",
            )}
          </p>
          {pending.request.title && (
            <p>
              <strong>{pending.request.title}</strong>
            </p>
          )}
          <form
            onSubmit={(event) => {
              event.preventDefault();
              void confirm();
            }}
          >
            <Field label={t("Current password")}>
              <input
                type="password"
                autoComplete="current-password"
                value={password}
                onChange={(event) => setPassword(event.target.value)}
                required
                disabled={busy}
              />
            </Field>
            <ErrorNotice error={error} />
            <div className="actions">
              <button disabled={busy || !password}>
                {t(busy ? "Verifying…" : "Verify and continue")}
              </button>
              <button type="button" className="secondary" disabled={busy} onClick={cancel}>
                {t("Cancel")}
              </button>
            </div>
          </form>
        </Dialog>
      )}
    </MutationContext.Provider>
  );
}
