import { useCallback, useEffect, useState } from "react";
import { api } from "./api";

export function useQuery<T>(path: string | null) {
  const [version, setVersion] = useState(0);
  const [state, setState] = useState<{
    path: string | null;
    data?: T;
    error?: Error;
    loading: boolean;
  }>({ path: null, loading: false });
  const reload = useCallback(() => setVersion((value) => value + 1), []);
  useEffect(() => {
    if (!path) return;
    const controller = new AbortController();
    setState({ path, loading: true });
    void api<T>(path, { signal: controller.signal })
      .then((data) => {
        if (!controller.signal.aborted) setState({ path, data, loading: false });
      })
      .catch((error: Error) => {
        if (!controller.signal.aborted) setState({ path, error, loading: false });
      });
    return () => controller.abort();
  }, [path, version]);
  if (state.path !== path)
    return { data: undefined, error: undefined, loading: Boolean(path), reload };
  return { ...state, reload };
}
