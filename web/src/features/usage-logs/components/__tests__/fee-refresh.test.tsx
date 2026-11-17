/* Copyright (C) 2023-2026 QuantumNous
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
import {
  focusManager,
  QueryClient,
  QueryClientProvider,
} from "@tanstack/react-query";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from "@tanstack/react-router";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";

import { api } from "@/lib/api";
import { STATUS_QUERY_KEY } from "@/lib/status-query";
import { useAuthStore } from "@/stores/auth-store";

import { usageLogSchema } from "../../data/schema";
import { UsageLogsProvider } from "../usage-logs-provider";
import { UsageLogsTable } from "../usage-logs-table";

let client: QueryClient;
afterEach(() => {
  cleanup();
  client?.clear();
  vi.useRealTimers();
  vi.restoreAllMocks();
  focusManager.setFocused(undefined);
  useAuthStore.getState().auth.reset();
});
it("refreshes only fees of retrieved rows while preserving search time and list", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval", "Date"] });
  vi.setSystemTime(new Date("2026-11-17T01:30:25Z"));
  vi.spyOn(window, "scrollTo").mockImplementation(() => {});
  useAuthStore.getState().auth.setUser({ id: 1, username: "preview", role: 1 });
  const row = usageLogSchema.parse({
    id: 1,
    user_id: 1,
    created_at: Math.floor(Date.now() / 1000),
    type: 2,
    content: "",
    model_name: "fixed-model",
    request_id: "request-1",
    ops_fee: { status: "waiting", amount: null, currency: "CNY" },
  });
  const lists: URLSearchParams[] = [];
  vi.spyOn(api, "get").mockImplementation(async (url) => {
    if (url.startsWith("/api/log/self?")) {
      lists.push(new URLSearchParams(url.split("?")[1]));
      return { data: { success: true, data: { items: [row], total: 1 } } };
    }
    return {
      data: {
        success: true,
        data: url.includes("/stat") ? { quota: 0, rpm: 0, tpm: 0 } : {},
      },
    };
  });
  const post = vi
    .spyOn(api, "post")
    .mockResolvedValueOnce({
      data: {
        success: true,
        data: [{ request_id: "request-1", ops_fee: row.ops_fee }],
      },
    })
    .mockResolvedValue({
      data: {
        success: true,
        data: [
          {
            request_id: "request-1",
            ops_fee: { status: "available", amount: "0.42", currency: "CNY" },
          },
        ],
      },
    });
  client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  });
  client.setQueryData(STATUS_QUERY_KEY, { ops_fees_enabled: true });
  const root = createRootRoute();
  const auth = createRoute({
    getParentRoute: () => root,
    id: "_authenticated",
  });
  const logs = createRoute({
    getParentRoute: () => auth,
    path: "/usage-logs/$section",
    validateSearch: (s: Record<string, unknown>) => s,
    component: () => (
      <UsageLogsProvider>
        <UsageLogsTable logCategory="common" />
      </UsageLogsProvider>
    ),
  });
  const router = createRouter({
    routeTree: root.addChildren([auth.addChildren([logs])]),
    history: createMemoryHistory({ initialEntries: ["/usage-logs/common"] }),
  });
  await router.load();
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  await screen.findByText("fixed-model");
  expect(lists[0].get("end_timestamp")).toBe(
    String(Math.floor(Date.now() / 1000)),
  );
  await act(async () => {
    await vi.advanceTimersByTimeAsync(2_000);
  });
  await waitFor(() => expect(screen.getByLabelText("¥0.42")).toBeVisible());
  expect(lists).toHaveLength(1);
  expect(post).toHaveBeenCalledWith(
    "/api/log/self/ops-fees",
    { request_ids: ["request-1"] },
    expect.objectContaining({ skipErrorHandler: true }),
  );
  expect(screen.getByRole("button", { name: "Search" })).toBeEnabled();
  expect(router.state.location.search).toEqual({});
  post.mockRejectedValue(new Error("temporary network failure"));
  await act(async () => {
    await vi.advanceTimersByTimeAsync(30_000);
  });
  expect(screen.getByLabelText("¥0.42")).toBeVisible();
  expect(lists).toHaveLength(1);
  const failedCalls = post.mock.calls.length;
  await act(async () => {
    await vi.advanceTimersByTimeAsync(10_000);
  });
  expect(post).toHaveBeenCalledTimes(failedCalls);
  focusManager.setFocused(false);
  await act(async () => {
    await vi.advanceTimersByTimeAsync(60_000);
  });
  expect(post).toHaveBeenCalledTimes(failedCalls);
  expect(lists).toHaveLength(1);
  focusManager.setFocused(true);
  await act(async () => {
    screen.getByRole("button", { name: "Search" }).click();
  });
  await waitFor(() => expect(lists.length).toBeGreaterThan(1));
  expect(lists.at(-1)?.get("end_timestamp")).toBe(
    lists[0].get("end_timestamp"),
  );
});
