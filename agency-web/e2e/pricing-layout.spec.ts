import { expect, test, type Locator, type Page } from "@playwright/test";

const channels = [
  {
    channel_id: 1,
    channel_name: "A very long distributor channel name that wraps across several lines",
  },
  { channel_id: 2, channel_name: "Second channel" },
];

async function openWorkspace(page: Page, root: boolean) {
  await page.route("**/agency/api/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname.replace("/agency/api/v1", "");
    let data: unknown = { items: [], total: 0 };
    if (path === "/auth/me")
      data = {
        actor_type: root ? "root" : "agency_operator",
        actor_id: 1,
        agency_id: root ? undefined : 7,
        username: "layout-operator",
        must_change_password: false,
      };
    if (path === "/root/sync/status")
      data = { schema: { ready: true }, capabilities: {}, backlog: { deliveries: {} } };
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

  test(`agency and customer pricing share plain channel rows and fallback logos at width ${width}`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 900 });
    await openWorkspace(page, false);
    await page.getByRole("button", { name: "Pricing", exact: true }).click();
    await verifyChannelAlignment(page.locator(".pricing-matrix"));
    await page.screenshot({ path: test.info().outputPath(`agency-${width}.png`), fullPage: true });
    await page.getByRole("button", { name: "Customers", exact: true }).click();
    await page.getByRole("button", { name: "Customer pricing", exact: true }).click();
    await verifyChannelAlignment(page.locator(".customer-pricing-matrix"));
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
