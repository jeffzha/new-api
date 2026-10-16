import { expect, test, type Locator, type Page } from "@playwright/test";

const channels = [
  {
    channel_id: 1,
    channel_name: "A very long distributor channel name that wraps across several lines",
  },
  { channel_id: 2, channel_name: "Second channel" },
];

async function openWorkspace(page: Page, root: boolean, acting = false) {
  await page.route("**/agency/api/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname.replace("/agency/api/v1", "");
    let data: unknown = { items: [], total: 0 };
    if (path === "/auth/me")
      data = {
        actor_type: root ? "root" : "agency_operator",
        actor_id: 1,
        agency_id: root && !acting ? undefined : 7,
        acting_agency: acting
          ? { display_name: "Managed agency", operator_username: "managed-operator" }
          : undefined,
        username: "layout-operator",
        must_change_password: false,
      };
    if (path === "/root/sync/status")
      data = {
        schema: { ready: true },
        capabilities: { billing_events: true },
        backlog: {
          deliveries: { pending: 2, retry: 1, completed: 50 },
          open_reconciliation_issues: 4,
        },
      };
    if (path === "/root/agencies") data = { items: [], total: 7 };
    if (path === "/root/customers") data = { items: [], total: 42 };
    if (path === "/root/platform-pricing")
      data = {
        revision: 1,
        refreshed_at_ms: 1,
        items: [
          {
            origin_model_name: "unlisted-model",
            channel_names: channels.map((channel) => channel.channel_name),
            channel_costs: channels.map((channel, index) => ({
              ...channel,
              available: true,
              platform_cost_bps: 5000 + index * 1000,
            })),
            platform_cost_bps: 5000,
            agency_cost_bps: 7000,
            child_cost_bps: 7500,
            default_sales_bps: 8000,
          },
        ],
      };
    if (path === "/pricing/model-sales")
      data = {
        agency_id: 7,
        agency_name: "Layout agency",
        revision: 1,
        platform_revision: 1,
        min_spread_bps: 0,
        default_sales_bps: 8000,
        items: [
          {
            origin_model_name: "unlisted-model",
            channels,
            agency_cost_bps: 7000,
            child_cost_bps: 7500,
            platform_default_sales_bps: 8000,
            sales_bps: 8000,
            override_sales_bps: null,
            override_child_cost_bps: null,
          },
        ],
      };
    if (path === "/customers")
      data = {
        items: [{ user_id: 42, username: "layout-customer", effective_at_ms: "1", revision: "1" }],
        total: 1,
      };
    if (path === "/customers/42/pricing")
      data = { default_sales_bps: 8000, default_agency_cost_bps: 7000, items: [] };
    if (path === "/models")
      data = { items: ["unlisted-model"], catalog: [{ model: "unlisted-model", channels }] };
    if (path === "/children")
      data = {
        items: [
          {
            id: 9,
            display_name: "Child agency",
            operator_username: "child",
            status: "active",
            depth: 1,
            price_revision: 3,
          },
        ],
      };
    if (path === "/hierarchy")
      data = {
        current: {
          id: 1,
          display_name: "Root agency",
          status: "active",
          depth: 0,
          price_revision: 1,
        },
        parents: [],
        children: [],
      };
    if (path === "/children/9/pricing")
      data = {
        revision: 3,
        default_settlement_bps: 7000,
        model_overrides: [
          {
            origin_model_name: "unlisted-model",
            agency_cost_bps: 7000,
            minimum_cost_bps: 7500,
            settlement_bps: 7000,
            channels,
          },
          {
            origin_model_name: "second-model",
            agency_cost_bps: 7200,
            minimum_cost_bps: 7500,
            settlement_bps: 7500,
            channels: [channels[1]],
          },
        ],
      };
    await route.fulfill({ json: { success: true, data } });
  });
  await page.goto("/agency/");
  await expect(page.getByRole("button", { name: "Sign out", exact: true })).toBeVisible();
}

async function verifyChannelAlignment(table: Locator) {
  await expect(table.locator(".model-name-cell .vendor-mark-monogram")).toHaveText("O");
  const channelCells = table.locator(".pricing-channel-cell");
  await expect(channelCells).toHaveCount(2);
  const rows = table.locator("tbody tr");
  for (let index = 0; index < channels.length; index++) {
    const row = rows.nth(index);
    const name = row.locator(".pricing-channel-cell");
    const cost = row.locator(".pricing-cost-cell");
    await expect(name).toHaveText(channels[index].channel_name);
    // Both cells share a table row, even when a channel name wraps.
    const nameBox = await name.boundingBox();
    const costBox = await cost.boundingBox();
    expect(nameBox?.y).toBe(costBox?.y);
    expect(nameBox?.height).toBe(costBox?.height);
    await expect(name.locator("svg, img, .channel-mark")).toHaveCount(0);
    expect(await name.evaluate((cell) => getComputedStyle(cell).textAlign)).toBe("left");
  }
  const headers = table.getByRole("columnheader");
  for (const [index, className] of [
    [2, ".pricing-channel-cell"],
    [3, ".pricing-cost-cell"],
  ] as const) {
    const header = await headers.nth(index).boundingBox();
    const cell = await table.locator(className).first().boundingBox();
    expect(header?.x).toBe(cell?.x);
    expect(
      await headers.nth(index).evaluate((element) => getComputedStyle(element).paddingLeft),
    ).toBe(
      await table
        .locator(className)
        .first()
        .evaluate((element) => getComputedStyle(element).paddingLeft),
    );
  }
}

test("root pricing renders configured models with null channel lists", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await openWorkspace(page, true);
  await page.route("**/agency/api/v1/root/platform-pricing", (route) =>
    route.fulfill({
      json: {
        success: true,
        data: {
          revision: 1,
          refreshed_at_ms: 1,
          items: [
            {
              origin_model_name: "unavailable-model",
              channel_names: null,
              channel_costs: null,
              agency_cost_bps: 7000,
              default_sales_bps: 9000,
            },
          ],
        },
      },
    }),
  );
  await page.getByRole("button", { name: "Pricing", exact: true }).click();
  await expect(
    page.locator(".pricing-matrix").getByText("unavailable-model", { exact: true }),
  ).toBeVisible();
  expect(errors).toEqual([]);
});

test("root pricing renders a null model list without crashing", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await openWorkspace(page, true);
  await page.route("**/agency/api/v1/root/platform-pricing", (route) =>
    route.fulfill({
      json: { success: true, data: { revision: 1, refreshed_at_ms: 1, items: null } },
    }),
  );
  await page.getByRole("button", { name: "Pricing", exact: true }).click();
  await expect(page.locator(".pricing-matrix")).toBeVisible();
  expect(errors).toEqual([]);
});

async function verifyAgencyPricingHidesChannels(table: Locator) {
  await expect(table.locator(".model-name-cell .vendor-mark-monogram")).toHaveText("O");
  await expect(table.getByRole("columnheader", { name: "Channels", exact: true })).toHaveCount(0);
  await expect(table.locator(".pricing-channel-cell")).toHaveCount(0);
  await expect(table.getByText(channels[0].channel_name, { exact: true })).toHaveCount(0);
  await expect(table.getByText(channels[1].channel_name, { exact: true })).toHaveCount(0);
}

for (const width of [1440, 390]) {
  test(`platform pricing aligns wrapped channels with costs and contains scrolling at width ${width}`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 900 });
    await openWorkspace(page, true);
    await page.getByRole("button", { name: "Pricing", exact: true }).click();
    await verifyChannelAlignment(page.locator(".pricing-matrix"));
    await expect(page.getByRole("tab", { name: /Other providers/ })).toBeVisible();
    const horizontalOverflow = await page.evaluate(
      () => document.documentElement.scrollWidth > window.innerWidth,
    );
    expect(horizontalOverflow).toBe(false);
    await page.screenshot({
      path: test.info().outputPath(`platform-${width}.png`),
      fullPage: true,
    });
  });

  test(`agency and customer pricing hide upstream channels at width ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await openWorkspace(page, false);
    await page.getByRole("button", { name: "Pricing", exact: true }).click();
    await verifyAgencyPricingHidesChannels(page.locator(".pricing-matrix"));
    await page.screenshot({ path: test.info().outputPath(`agency-${width}.png`), fullPage: true });
    await page.getByRole("button", { name: "Customers", exact: true }).click();
    await page.getByRole("button", { name: "Customer pricing", exact: true }).click();
    await verifyAgencyPricingHidesChannels(page.locator(".customer-pricing-matrix"));
    await page.getByLabel("Select model: unlisted-model", { exact: true }).check();
    await page.getByRole("button", { name: "Increase/decrease sales prices", exact: true }).click();
    const dialog = page.getByRole("dialog");
    await expect(
      dialog.getByRole("columnheader", { name: "After adjustment", exact: true }),
    ).toBeVisible();
    await page.screenshot({
      path: test.info().outputPath(`customer-${width}.png`),
      fullPage: true,
    });
  });
}

test("reconciliation keeps checks and history visible while hiding service status blocks", async ({
  page,
}) => {
  await openWorkspace(page, true);
  await page.getByRole("button", { name: "Sync & reconciliation", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Reconciliation", exact: true })).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "Reconciliation history", exact: true }),
  ).toBeVisible();
  for (const name of ["Service status", "Capability", "Pending work"]) {
    await expect(page.getByRole("heading", { name, exact: true })).toHaveCount(0);
  }
});

test("child pricing uses the shared search and provider tabs without dropping untouched edits", async ({
  page,
}) => {
  await openWorkspace(page, false);
  await page.getByRole("button", { name: "Agencies", exact: true }).click();
  await page.getByRole("button", { name: "Manage child pricing", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(
    dialog.getByPlaceholder("Search models or providers", { exact: true }),
  ).toBeVisible();
  const input = dialog.getByLabel("Child cost coefficient: unlisted-model", { exact: true });
  await expect(dialog.getByText("Minimum child cost: 0.7500", { exact: true })).toHaveCount(2);
  await input.fill("0.7800");
  await dialog.getByPlaceholder("Search models or providers", { exact: true }).fill("unlisted");
  await expect(input).toHaveValue("0.7800");
  const published = page.waitForRequest(
    (request) =>
      request.method() === "POST" && request.url().endsWith("/children/9/pricing/publish"),
  );
  await dialog.getByRole("button", { name: "Publish child pricing", exact: true }).click();
  expect((await published).postDataJSON().model_cost_overrides).toEqual([
    { origin_model_name: "unlisted-model", settlement_bps: 7800 },
  ]);
});

test("root overview restores totals, capabilities and unfinished queue counts", async ({
  page,
}) => {
  await openWorkspace(page, true);
  await expect(page.getByRole("heading", { name: "Service status", exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Capability", exact: true })).toBeVisible();
  await expect(
    page.locator(".commission-metrics article").filter({ hasText: "Agencies" }).locator("strong"),
  ).toHaveText("7");
  await expect(
    page.locator(".commission-metrics article").filter({ hasText: "Customers" }).locator("strong"),
  ).toHaveText("42");
  await expect(
    page.locator(".status-kpi").filter({ hasText: "Pending work" }).locator("strong"),
  ).toHaveText("3");
  const totals = await page.locator(".commission-metrics").boundingBox();
  const serviceStatus = await page
    .getByRole("heading", { name: "Service status", exact: true })
    .boundingBox();
  expect(serviceStatus!.y - (totals!.y + totals!.height)).toBeGreaterThanOrEqual(16);
});

test("root acting as an agency gets the ordinary agency pricing experience", async ({ page }) => {
  await openWorkspace(page, true, true);
  const pricingRequest = page.waitForRequest((request) =>
    request.url().endsWith("/agency/api/v1/pricing/model-sales"),
  );
  await page.getByRole("button", { name: "Pricing", exact: true }).click();
  await pricingRequest;
  await expect(page.getByPlaceholder("Search models or providers", { exact: true })).toBeVisible();
  await expect(page.getByRole("columnheader", { name: "Channels", exact: true })).toHaveCount(0);
  await expect(page.locator(".pricing-channel-cell")).toHaveCount(0);
  await expect(page.getByRole("button", { name: /All channels/ })).toHaveCount(0);
});

test("publishing one agency price does not submit unchanged automatic or manual prices", async ({
  page,
}) => {
  await openWorkspace(page, false);
  await page.route("**/agency/api/v1/pricing/model-sales", (route) =>
    route.fulfill({
      json: {
        success: true,
        data: {
          agency_id: 7,
          agency_name: "Layout agency",
          revision: 1,
          platform_revision: 1,
          min_spread_bps: 0,
          default_sales_bps: 10000,
          items: [
            {
              origin_model_name: "automatic-model",
              agency_cost_bps: 7000,
              child_cost_bps: 7500,
              sales_bps: 10000,
              override_sales_bps: null,
              override_child_cost_bps: null,
              sales_source: "default",
              child_cost_source: "default",
            },
            {
              origin_model_name: "manual-model",
              agency_cost_bps: 7000,
              child_cost_bps: 8000,
              sales_bps: 9000,
              override_sales_bps: 9000,
              override_child_cost_bps: 8000,
              sales_source: "manual",
              child_cost_source: "manual",
            },
          ],
        },
      },
    }),
  );
  await page.getByRole("button", { name: "Pricing", exact: true }).click();
  const table = page.locator(".pricing-matrix");
  await expect(
    table
      .getByRole("row")
      .filter({ hasText: "automatic-model" })
      .getByText("Agency override", { exact: true }),
  ).toHaveCount(0);
  await page.getByLabel("Sales price (coefficient): manual-model", { exact: true }).fill("0.9500");
  const published = page.waitForRequest(
    (request) => request.method() === "POST" && request.url().endsWith("/pricing/sales/publish"),
  );
  await page.getByRole("button", { name: "Publish sales coefficients", exact: true }).click();
  const payload = (await published).postDataJSON();
  expect(payload.model_sales_overrides).toEqual([
    { origin_model_name: "manual-model", sales_bps: 9500 },
  ]);
  expect(payload.model_child_cost_overrides).toEqual([]);
});

for (const width of [1440, 390]) {
  test(`child cost dialog contains its table and bulk preview at width ${width}`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 1000 });
    await openWorkspace(page, false);
    await page.getByRole("button", { name: "Agencies", exact: true }).click();
    await page.getByRole("button", { name: "Manage child pricing", exact: true }).click();
    const dialog = page.locator(".child-pricing-dialog");
    const box = await dialog.boundingBox();
    expect(box!.width).toBeLessThanOrEqual(width);
    if (width > 1000) {
      expect(box!.width).toBeGreaterThan(1100);
      expect(
        await dialog
          .locator(".child-pricing-matrix-wrap")
          .evaluate((element) => element.scrollWidth <= element.clientWidth),
      ).toBe(true);
    }
    await dialog.getByLabel("Child cost coefficient: second-model", { exact: true }).fill("0.7600");
    await dialog.getByLabel("Select model: unlisted-model", { exact: true }).check();
    await dialog.getByRole("button", { name: "Increase/decrease child cost", exact: true }).click();
    const adjust = page.locator(".coefficient-adjust-dialog");
    await expect(adjust.getByRole("button", { name: "From my cost", exact: true })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    await expect(adjust.locator(".coefficient-adjust-next")).toHaveText("0.8000");
    await adjust.getByRole("button", { name: "+0.1", exact: true }).click();
    await expect(adjust.locator(".coefficient-adjust-next")).toHaveText("0.9000");
    await adjust.getByRole("button", { name: "-0.1", exact: true }).click();
    await expect(adjust.locator(".coefficient-adjust-next")).toHaveText("0.8000");
    await page.screenshot({
      path: test.info().outputPath(`child-adjust-${width}.png`),
      fullPage: true,
    });
    await adjust.getByRole("button", { name: "Apply to selected models", exact: true }).click();
    await expect(
      dialog.getByLabel("Child cost coefficient: unlisted-model", { exact: true }),
    ).toHaveValue("0.8000");
    await expect(
      dialog.getByLabel("Child cost coefficient: second-model", { exact: true }),
    ).toHaveValue("0.7600");
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth),
    ).toBe(false);
  });
}
