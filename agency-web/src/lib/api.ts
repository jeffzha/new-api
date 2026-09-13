export interface HubConfig {
  base_path: string;
  platform_base_url: string;
}
declare global {
  interface Window {
    __AGENCY_CONFIG__?: HubConfig;
  }
}

export function hubConfig(): HubConfig {
  const config = window.__AGENCY_CONFIG__;
  return {
    base_path: config?.base_path?.replace(/\/$/, "") || "/agency",
    platform_base_url: config?.platform_base_url || "",
  };
}

export class ApiError extends Error {
  constructor(
    message: string,
    public status: number,
    public code: string,
    public details?: unknown,
  ) {
    super(message);
  }
}

export function csrfToken(): string {
  return decodeURIComponent(
    document.cookie
      .split("; ")
      .find((value) => value.startsWith("agency_csrf="))
      ?.split("=")
      .slice(1)
      .join("=") || "",
  );
}

export async function api<T = unknown>(path: string, options: RequestInit = {}): Promise<T> {
  const headers = new Headers(options.headers);
  headers.set("Content-Type", "application/json");
  if (options.method && options.method !== "GET") headers.set("X-CSRF-Token", csrfToken());
  const response = await fetch(hubConfig().base_path + "/api/v1" + path, {
    ...options,
    headers,
    credentials: "include",
    cache: "no-store",
  });
  const body = await response.json().catch(() => null);
  if (!response.ok || !body?.success) {
    throw new ApiError(
      body?.error?.message || body?.message || `HTTP ${response.status}`,
      response.status,
      body?.error?.code || "invalid_response",
      body?.error?.details,
    );
  }
  return body.data as T;
}

// Go's canonical JSON uses sorted keys and escapes HTML characters. Start
// from the actual wire body so undefined fields cannot change the proof hash.
export function canonicalBody(body: unknown): string {
  const wire = JSON.parse(
    JSON.stringify(body, (_key, value: unknown) => {
      if (typeof value === "number" && !Number.isSafeInteger(value))
        throw new Error("Use an exact integer or a decimal string.");
      return value;
    }),
  ) as unknown;
  function encode(value: unknown): string {
    if (typeof value === "number" && !Number.isSafeInteger(value))
      throw new Error("Use an exact integer or a decimal string.");
    if (Array.isArray(value)) return "[" + value.map(encode).join(",") + "]";
    if (value && typeof value === "object") {
      const record = value as Record<string, unknown>;
      return (
        "{" +
        Object.keys(record)
          .sort()
          .map((key) => JSON.stringify(key) + ":" + encode(record[key]))
          .join(",") +
        "}"
      );
    }
    return JSON.stringify(value);
  }
  return encode(wire).replace(
    /[<>&\u2028\u2029]/g,
    (char) => "\\u" + char.charCodeAt(0).toString(16).padStart(4, "0"),
  );
}

export async function bodyHash(body: unknown): Promise<string> {
  const bytes = new TextEncoder().encode(canonicalBody(body));
  const hash = await crypto.subtle.digest("SHA-256", bytes);
  return Array.from(new Uint8Array(hash), (byte) => byte.toString(16).padStart(2, "0")).join("");
}
