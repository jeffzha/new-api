/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo } from "react";

import { api } from "@/lib/api";
import { useAuthStore } from "@/stores/auth-store";

import type { UsageLog } from "../data/schema";

type FeeRow = { request_id: string; ops_fee: UsageLog["ops_fee"] };
type FeeSnapshot = { rows: FeeRow[]; pendingSince: Record<string, number> };

export function useLogFees<T extends object>(
  rows: T[],
  isAdmin: boolean,
  enabled: boolean,
  snapshot = 0,
): T[] {
  const client = useQueryClient();
  const userId = useAuthStore((state) => state.auth.user?.id);
  const ids = useMemo(
    () =>
      [
        ...new Set(
          rows.flatMap((row) => {
            const log = row as Partial<UsageLog>;
            return log.type === 2 && log.ops_fee && log.request_id
              ? [log.request_id]
              : [];
          }),
        ),
      ].slice(0, 100),
    [rows],
  );
  const queryKey = ["usage-log-fees", userId, isAdmin, ids, snapshot];
  const query = useQuery({
    queryKey,
    enabled: enabled && ids.length > 0,
    queryFn: async ({ signal }) => {
      const res = await api.post<{ success: boolean; data: FeeRow[] }>(
        isAdmin ? "/api/log/ops-fees" : "/api/log/self/ops-fees",
        { request_ids: ids },
        { signal, skipErrorHandler: true },
      );
      if (!res.data.success) throw new Error("Fee refresh unavailable");
      const previous = client.getQueryData<FeeSnapshot>(queryKey);
      const received = new Map(
        res.data.data.map((row) => [row.request_id, row.ops_fee]),
      );
      const last = new Map(
        previous?.rows.map((row) => [row.request_id, row.ops_fee]),
      );
      const initial = new Map(
        rows.map((row) => {
          const log = row as Partial<UsageLog>;
          return [log.request_id, log.ops_fee];
        }),
      );
      const pendingSince: Record<string, number> = {};
      const fees = ids.map((id) => {
        // A temporarily omitted row is not evidence that its fee completed.
        const fee = received.get(id) ?? last.get(id) ?? initial.get(id);
        if (fee?.status === "waiting") {
          pendingSince[id] = previous?.pendingSince[id] ?? Date.now();
        }
        return { request_id: id, ops_fee: fee };
      });
      return { rows: fees, pendingSince };
    },
    retry: false,
    meta: { errorToast: false },
    refetchInterval: (query) => {
      if (query.state.status === "error") return 30_000;
      const pending = query.state.data
        ? query.state.data.rows.some((row) => row.ops_fee?.status === "waiting")
        : rows.some(
            (row) => (row as Partial<UsageLog>).ops_fee?.status === "waiting",
          );
      if (!pending) return 30_000;
      const waiting = Object.values(query.state.data?.pendingSince ?? {});
      const age = waiting.length ? Date.now() - Math.max(...waiting) : 0;
      // A new request gets its own short fast window; old rows cannot consume it.
      if (age < 2_000) return 250;
      if (age < 10_000) return 500;
      if (age < 30_000) return 1_000;
      if (age < 120_000) return 5_000;
      return 30_000;
    },
    refetchIntervalInBackground: false,
  });
  return useMemo(() => {
    if (!enabled || !query.data) return rows;
    const fees = new Map(
      query.data.rows.map((row) => [row.request_id, row.ops_fee]),
    );
    return rows.map((row) => {
      const log = row as Partial<UsageLog>;
      const fee = log.request_id ? fees.get(log.request_id) : undefined;
      return log.type === 2 && fee ? { ...row, ops_fee: fee } : row;
    });
  }, [rows, enabled, query.data]);
}
