import { useCallback, useState } from "react";
import { useMutation as useMutationRequest } from "./mutation-context";
import type { MutationRequest } from "./types";

export function useMutation() {
  const request = useMutationRequest();
  const [pending, setPending] = useState(false);
  const mutate = useCallback(
    async <T = unknown>(
      path: string,
      body: unknown,
      options: Omit<MutationRequest, "path" | "body"> = {},
    ): Promise<T> => {
      setPending(true);
      try {
        return await request<T>({ path, body, ...options });
      } finally {
        setPending(false);
      }
    },
    [request],
  );
  return { mutate, pending };
}
