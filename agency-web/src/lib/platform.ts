import { hubConfig } from "./api";
import type { VerificationPayload } from "./bridge-contract";

interface BridgeMessage {
  source: string;
  ready?: boolean;
  request_id?: string;
  ok?: boolean;
  proof?: string;
  ticket?: string;
  error?: string;
}

function responseSource(mode: "sso" | "verify" | "link"): string {
  if (mode === "sso") return "new-api-agency-sso";
  if (mode === "verify") return "new-api-agency-verification";
  return "new-api-agency-link";
}

function requestSource(mode: "verify" | "link"): string {
  return mode === "verify" ? "agency-hub-verification" : "agency-hub-account-link";
}

function platformBridge(
  mode: "sso" | "verify" | "link",
  stateHash?: string,
  payload?: VerificationPayload,
  signal?: AbortSignal,
): Promise<{ value: string; requestId: string }> {
  return new Promise((resolve, reject) => {
    const platform = new URL(hubConfig().platform_base_url || location.origin);
    if (
      !["https:", "http:"].includes(platform.protocol) ||
      platform.username ||
      platform.password
    ) {
      reject(new Error("Invalid platform address."));
      return;
    }
    const iframe = document.createElement("iframe");
    iframe.hidden = true;
    iframe.title = "Platform authentication";
    const requestId = crypto.randomUUID();
    const url = new URL("/api/agency/sso", platform);
    url.searchParams.set("origin", location.origin);
    if (mode !== "sso") url.searchParams.set("mode", mode);
    if (stateHash) url.searchParams.set("state_hash", stateHash);
    iframe.src = url.href;
    let sent = false;
    const timer = window.setTimeout(
      () => finish(undefined, new Error("Platform authentication timed out. Try again.")),
      30000,
    );
    function finish(value?: string, error?: Error) {
      clearTimeout(timer);
      window.removeEventListener("message", receive);
      signal?.removeEventListener("abort", abort);
      iframe.remove();
      if (error) reject(error);
      else resolve({ value: value || "", requestId });
    }
    function abort() {
      finish(undefined, new Error("Operation cancelled."));
    }
    function receive(event: MessageEvent<BridgeMessage>) {
      if (event.origin !== platform.origin || event.source !== iframe.contentWindow) return;
      const data = event.data;
      if (!data || data.source !== responseSource(mode)) return;
      if (mode !== "sso" && data.ready && !sent) {
        sent = true;
        iframe.contentWindow?.postMessage(
          {
            source: requestSource(mode),
            request_id: requestId,
            payload,
          },
          platform.origin,
        );
        return;
      }
      if (mode !== "sso" && data.request_id !== requestId) return;
      if (!data.ok) {
        finish(undefined, new Error(data.error || "Platform authentication failed."));
        return;
      }
      const value = mode === "sso" ? data.ticket : data.proof;
      if (!value) finish(undefined, new Error("Invalid authentication response."));
      else finish(value);
    }
    if (signal?.aborted) {
      abort();
      return;
    }
    signal?.addEventListener("abort", abort, { once: true });
    window.addEventListener("message", receive);
    iframe.onerror = () => finish(undefined, new Error("Platform authentication failed."));
    document.body.appendChild(iframe);
  });
}

export function requestRootProof(
  payload: VerificationPayload,
  signal?: AbortSignal,
): Promise<string> {
  return platformBridge("verify", undefined, payload, signal).then((result) => result.value);
}

export function requestPlatformLinkProof(
  signal?: AbortSignal,
): Promise<{ proof: string; requestId: string }> {
  return platformBridge("link", undefined, undefined, signal).then((result) => ({
    proof: result.value,
    requestId: result.requestId,
  }));
}

export async function startPlatformSSO(signal?: AbortSignal): Promise<void> {
  const base = hubConfig().base_path;
  const startResponse = await fetch(base + "/sso/start", {
    credentials: "include",
    signal,
    cache: "no-store",
  });
  const start = await startResponse.json();
  if (!startResponse.ok || !start.success || !start.data?.state_hash)
    throw new Error(start.error?.message || "Platform authentication failed.");
  const ticket = (await platformBridge("sso", start.data.state_hash, undefined, signal)).value;
  const response = await fetch(base + "/sso/callback", {
    method: "POST",
    credentials: "include",
    signal,
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ ticket, state: start.data.state }),
  });
  const result = await response.json();
  if (!response.ok || !result.success)
    throw new Error(result.error?.message || "Platform authentication failed.");
}
