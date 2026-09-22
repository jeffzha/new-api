import { useCallback, useRef } from "react";
import type { ReactNode } from "react";
import { ApiError, api, canonicalBody } from "../lib/api";
import { MutationContext } from "../lib/mutation-context";
import type { Identity, MutationRequest, Mutate } from "../lib/types";

/** Mutations rely on the active session; expired sessions return 401 and require login again. */
export function MutationProvider(props: { identity: Identity; children: ReactNode }) {
  const operationKeys = useRef(new Map<string, string>());
  const execute = useCallback(async (request: MutationRequest, key: string) => {
    const data = await api<unknown>(request.path, {
      method: request.method || "POST",
      body: canonicalBody(request.body),
      headers: { "Idempotency-Key": key },
    });
    if (data && typeof data === "object" && "operation_id" in data && "status" in data && data.status === "processing") {
      throw new ApiError("The operation is still processing. Retry with the same details.", 202, "processing");
    }
    return data;
  }, []);
  const mutate: Mutate = useCallback(async <T,>(request: MutationRequest): Promise<T> => {
    const signature = String(props.identity.actor_id) + ":" + String(props.identity.agency_id) + ":" + (request.method || "POST") + ":" + request.path + ":" + canonicalBody(request.body);
    const key = operationKeys.current.get(signature) || crypto.randomUUID();
    operationKeys.current.set(signature, key);
    try {
      const result = await execute(request, key);
      operationKeys.current.delete(signature);
      return result as T;
    } catch (cause) {
      if (cause instanceof ApiError && cause.status >= 400 && cause.status < 500) operationKeys.current.delete(signature);
      throw cause;
    }
  }, [execute, props.identity.actor_id, props.identity.agency_id]);
  return <MutationContext.Provider value={mutate}>{props.children}</MutationContext.Provider>;
}
