import { expect, test, type Page } from "@playwright/test";
import { readFile, writeFile } from "node:fs/promises";

const operatorPassword = "Browser-operator-2026!";
const rootPassword = "Browser-root-2026!";

async function rootLogin(page: Page) {
  const response = await page.request.post("/__fixture/root");
  expect(response.ok()).toBeTruthy();
  await page.goto("/agency/");
  await expect(page.getByRole("button", { name: "Sign out", exact: true })).toBeVisible();
}

async function operatorLogin(page: Page, username = "browser-operator") {
  await page.goto("/agency/");
  await page.getByLabel("Username", { exact: true }).fill(username);
  await page.getByLabel("Password", { exact: true }).fill(operatorPassword);
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page.getByRole("button", { name: "Sign out", exact: true })).toBeVisible();
}

async function confirm(page: Page, password: string) {
  const dialog = page.getByRole("dialog", {
    name: "Confirm this action",
    exact: true,
  });
  await expect(dialog).toBeVisible();
  await dialog.getByLabel("Current password", { exact: true }).fill(password);
  await dialog.getByRole("button", { name: "Verify and continue", exact: true }).click();
}

test("operator copies its invitation and downloads the QR code after a recoverable image error", async ({
  page,
}) => {
  await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
  let rejectFirstQR = true;
  await page.route("**/api/v1/public/invitations/*/qr", async (route) => {
    if (rejectFirstQR) {
      rejectFirstQR = false;
      await route.fulfill({ status: 503, body: "temporarily unavailable" });
      return;
    }
    await route.continue();
  });
  await operatorLogin(page);
  await page.getByRole("button", { name: "Invite customers", exact: true }).click();
  const response = await page.request.get("/agency/api/v1/invitation");
  expect(response.ok()).toBeTruthy();
  const { data: invitation } = await response.json();
  expect(invitation.display_name).toBe("Browser Agency");
  expect(new URL(invitation.invite_url).searchParams.get("invite")).toBe(invitation.invite_code);
  await expect(page.getByRole("textbox", { name: "Invitation link", exact: true })).toHaveValue(
    invitation.invite_url,
  );
  await expect(
    page.getByRole("link", { name: "Open registration page", exact: true }),
  ).toHaveAttribute("href", invitation.invite_url);
  await page.getByRole("button", { name: "Copy invitation link", exact: true }).click();
  await expect(page.getByText("Invitation link copied.", { exact: true })).toBeVisible();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(invitation.invite_url);
  await expect(
    page.getByText("The invitation QR code could not be loaded. Try again.", {
      exact: true,
    }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Retry QR code", exact: true }).click();
  const qrImage = page.getByRole("img", {
    name: "Registration QR code for Browser Agency",
    exact: true,
  });
  await expect(qrImage).toBeVisible();
  await expect
    .poll(() => qrImage.evaluate((element) => (element as HTMLImageElement).naturalWidth))
    .toBe(512);
  const pendingDownload = page.waitForEvent("download");
  await page.getByRole("link", { name: "Download QR code (PNG)", exact: true }).click();
  const download = await pendingDownload;
  expect(download.suggestedFilename()).toBe("agency-invitation.png");
  const qrFile = test.info().outputPath("agency-invitation.png");
  await download.saveAs(qrFile);
  expect((await readFile(qrFile)).subarray(0, 8)).toEqual(
    Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]),
  );
  await writeFile(
    test.info().outputPath("invitation-target.json"),
    JSON.stringify(invitation, null, 2),
  );
  await page.evaluate(() => {
    Object.defineProperty(navigator.clipboard, "writeText", {
      configurable: true,
      value: async () => {
        throw new Error("Clipboard permission denied");
      },
    });
  });
  await page.getByRole("button", { name: "Copy invitation link", exact: true }).click();
  await expect(
    page.getByText("Copy failed. Select and copy the full invitation link above.", { exact: true }),
  ).toBeVisible();
  await expect(page.getByRole("textbox", { name: "Invitation link", exact: true })).toHaveValue(
    invitation.invite_url,
  );
  await page.getByLabel("Language", { exact: true }).selectOption("zh");
  await page.screenshot({
    path: test.info().outputPath("invitation-page-zh.png"),
    fullPage: true,
  });
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(
    true,
  );
  await page.screenshot({
    path: test.info().outputPath("invitation-page-mobile-zh.png"),
    fullPage: true,
  });
});

test("operator sees net lifetime commission without deducting paid or locked amounts twice", async ({
  page,
}) => {
  await operatorLogin(page, "browser-summary");
  const response = await page.request.get("/agency/api/v1/commissions/summary");
  expect(response.ok()).toBeTruthy();
  const { data } = await response.json();
  expect(data.items).toHaveLength(2);
  expect(data.items[0]).toMatchObject({
    currency_code: "CNY",
    net_earned_micros: "800000000",
  });
  expect(data.items[1]).toMatchObject({
    currency_code: "USD",
    net_earned_micros: "9007199254740992",
  });

  const cny = page.getByRole("heading", { name: "CNY", exact: true }).locator("..");
  for (const [label, value] of [
    ["Available commission", "CNY 400.00"],
    ["Withdrawn commission", "CNY 300.00"],
    ["Net total commission", "CNY 800.00"],
    ["Total earned commission", "CNY 1,000.00"],
    ["Reversed commission", "CNY 200.00"],
    ["Locked commission", "CNY 100.00"],
  ]) {
    await expect(
      cny
        .locator("article")
        .filter({ has: page.getByText(label, { exact: true }) })
        .locator("strong"),
    ).toHaveText(value);
  }
  const usd = page.getByRole("heading", { name: "USD", exact: true }).locator("..");
  await expect(
    usd
      .locator("article")
      .filter({ has: page.getByText("Net total commission", { exact: true }) })
      .locator("strong"),
  ).toHaveText("USD 9,007,199,254.740992");
  await page.getByRole("button", { name: "Refresh", exact: true }).click();
  await expect(
    cny
      .locator("article")
      .filter({ has: page.getByText("Net total commission", { exact: true }) })
      .locator("strong"),
  ).toHaveText("CNY 800.00");
  await page.getByLabel("Language", { exact: true }).selectOption("zh");
  await expect(page.getByText("净佣金总额", { exact: true })).toHaveCount(2);
  await page.screenshot({
    path: test.info().outputPath("commission-overview-zh.png"),
    fullPage: true,
  });
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(
    true,
  );
  await page.screenshot({
    path: test.info().outputPath("commission-overview-mobile-zh.png"),
    fullPage: true,
  });
});

test("root creates an agency, acknowledges delivery and the operator must change its temporary password", async ({
  page,
  browser,
}) => {
  await rootLogin(page);
  await page.getByRole("button", { name: "Agencies", exact: true }).click();
  await page.getByRole("button", { name: "Create agency", exact: true }).click();
  await page.getByLabel("Agency name", { exact: true }).fill("Browser Created Agency");
  await page.getByLabel("Operator username", { exact: true }).fill("browser-created-operator");
  await expect(page.getByLabel("Default sales coefficient", { exact: true })).toHaveCount(0);
  await expect(page.getByText("Pricing is inherited from the platform pricing policy after the agency is created.", { exact: true })).toBeVisible();
  const createResponse = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" && response.url().endsWith("/api/v1/root/agencies"),
  );
  await page.locator("form").getByRole("button", { name: "Create agency", exact: true }).click();
  await confirm(page, rootPassword);
  expect((await createResponse).status()).toBe(201);
  const password = page.getByLabel("Temporary password", { exact: true });
  await expect(password).not.toHaveValue("");
  const temporaryPassword = await password.inputValue();
  await expect(page.getByLabel("Invitation link", { exact: true })).toHaveValue(
    /\/register\?invite=/,
  );
  await expect(page.getByRole("button", { name: "Close", exact: true })).toBeDisabled();
  const acknowledgement = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      /\/root\/deliveries\/\d+\/ack$/.test(response.url()),
  );
  await page.getByRole("button", { name: "Confirm receipt", exact: true }).click();
  await confirm(page, rootPassword);
  expect((await acknowledgement).status()).toBe(200);
  await expect(page.getByRole("status")).toContainText("Receipt confirmed");
  await page.getByRole("button", { name: "Close", exact: true }).click();
  await expect(password).toHaveCount(0);
  await expect(page.getByRole("row").filter({ hasText: "Browser Created Agency" })).toBeVisible();
  const state = await (await page.request.get("/__fixture/state")).json();
  expect(state.deliveries).toEqual([
    { id: expect.any(Number), ciphertext_present: false, delivered: true },
  ]);
  await page.getByRole("button", { name: "Audit log", exact: true }).click();
  await expect(
    page.getByRole("cell", { name: "新增代理商", exact: true }).first(),
  ).toBeVisible();
  await expect(
    page.getByRole("cell", { name: "创建代理商账号", exact: true }).first(),
  ).toBeVisible();
  await page.getByRole("button", { name: "Agencies", exact: true }).click();
  await page.getByLabel("Language", { exact: true }).selectOption("zh");
  await page.screenshot({
    path: test.info().outputPath("agency-management-zh.png"),
    fullPage: true,
  });
  const operatorContext = await browser.newContext({
    locale: "en-US",
    baseURL: test.info().project.use.baseURL,
  });
  const operator = await operatorContext.newPage();
  try {
    await operator.goto("/agency/");
    await operator.getByLabel("Username", { exact: true }).fill("browser-created-operator");
    await operator.getByLabel("Password", { exact: true }).fill(temporaryPassword);
    await operator.getByRole("button", { name: "Sign in", exact: true }).click();
    await expect(
      operator.getByText("Change your temporary password before continuing.", {
        exact: true,
      }),
    ).toBeVisible();
    expect((await operator.request.get("/agency/api/v1/pricing")).status()).toBe(403);
    await operator.getByLabel("Current password", { exact: true }).fill(temporaryPassword);
    await operator.getByLabel("New password", { exact: true }).fill("Browser-2026!");
    await operator.getByLabel("Confirm password", { exact: true }).fill("Browser-2026!");
    await operator.getByRole("button", { name: "Save password", exact: true }).click();
    await expect(operator.getByRole("navigation", { name: "Agency navigation" })).toBeVisible();
    expect((await operator.request.get("/agency/api/v1/pricing")).status()).toBe(200);
    expect((await operator.request.get("/agency/api/v1/root/agencies")).status()).toBe(403);
  } finally {
    await operatorContext.close();
  }
});

test("root publishes platform pricing and an operator manages direct-child and customer sales pricing", async ({
  page,
  browser,
}) => {
  await rootLogin(page);
  await page.getByRole("button", { name: "Pricing", exact: true }).click();
  await expect(page.getByRole("cell", { name: "browser-chat-model", exact: true })).toBeVisible();
  await expect(page.getByText("Browser model channel", { exact: true })).toBeVisible();
  await page.getByLabel("Platform cost coefficient: browser-chat-model", { exact: true }).fill("0.75");
  await page.getByLabel("Agency cost coefficient: browser-chat-model", { exact: true }).fill("0.80");
  await page.getByLabel("Sales coefficient: browser-chat-model", { exact: true }).fill("0.90");
  await page.getByLabel("Change reason", { exact: true }).first().fill("Browser platform pricing");
  const platformPublished = page.waitForResponse((response) =>
    response.url().endsWith("/root/platform-pricing/publish"),
  );
  await page.getByRole("button", { name: "Publish platform pricing", exact: true }).click();
  await confirm(page, rootPassword);
  const platformResponse = await platformPublished;
  expect(platformResponse.status()).toBe(200);
  expect(platformResponse.request().postDataJSON()).toMatchObject({
    expected_revision: 0,
    model_prices: [{
      origin_model_name: "browser-chat-model",
      platform_cost_bps: 7500,
      agency_cost_bps: 8000,
      default_sales_bps: 9000,
    }],
  });

  const operatorContext = await browser.newContext({
    locale: "en-US",
    baseURL: test.info().project.use.baseURL,
  });
  const operator = await operatorContext.newPage();
  try {
    await operatorLogin(operator);
    await operator.getByRole("button", { name: "Pricing", exact: true }).click();
    const row = operator.getByRole("row").filter({ hasText: "browser-chat-model" });
    await expect(row.getByText("0.8000", { exact: true })).toBeVisible();
    const sales = operator.getByLabel("Sales coefficient: browser-chat-model", { exact: true });
    await expect(sales).toHaveValue("");
    await expect(sales).toHaveAttribute("placeholder", "0.9000");
    await sales.fill("0.95");
    const childCost = operator.getByLabel("Child agency cost coefficient: browser-chat-model", { exact: true });
    await expect(childCost).toHaveAttribute("placeholder", "Not configured");
    await childCost.fill("0.85");
    await operator.getByLabel("Change reason", { exact: true }).fill("Browser agency sales override");
    const published = operator.waitForResponse((response) =>
      response.url().endsWith("/pricing/sales/publish"),
    );
    await operator.getByRole("button", { name: "Publish sales coefficients", exact: true }).click();
    await confirm(operator, operatorPassword);
    const publishResponse = await published;
    expect(publishResponse.status()).toBe(200);
    expect(publishResponse.request().postDataJSON()).toMatchObject({
      expected_revision: 1,
      model_sales_overrides: [{
        origin_model_name: "browser-chat-model",
        sales_bps: 9500,
      }],
      model_child_cost_overrides: [{
        origin_model_name: "browser-chat-model",
        child_cost_bps: 8500,
      }],
    });
    expect(publishResponse.request().postDataJSON()).not.toHaveProperty("default_settlement_bps");
    const current = await (await operator.request.get("/agency/api/v1/pricing/model-sales")).json();
    expect(current.data).toMatchObject({
      revision: 2,
      items: [{
        origin_model_name: "browser-chat-model",
        agency_cost_bps: 8000,
        platform_default_sales_bps: 9000,
        sales_bps: 9500,
        override_sales_bps: 9500,
        child_cost_bps: 8500,
        override_child_cost_bps: 8500,
      }],
    });
    await operator.getByRole("button", { name: "Agencies", exact: true }).click();
    await expect(operator.getByRole("heading", { name: "Agency hierarchy", exact: true })).toBeVisible();
    await operator.getByText("Direct child agencies", { exact: true }).scrollIntoViewIfNeeded();
    await operator.getByRole("button", { name: "Create child agency", exact: true }).click();
    const createChild = operator.getByRole("dialog", { name: "Create child agency", exact: true });
    await createChild.getByLabel("Agency name", { exact: true }).fill("Browser Child Agency");
    await createChild.getByLabel("Operator username", { exact: true }).fill("browser-child-operator");
    const createdChild = operator.waitForResponse((response) =>
      response.request().method() === "POST" && response.url().endsWith("/api/v1/children"),
    );
    await createChild.getByRole("button", { name: "Create child agency", exact: true }).click();
    await confirm(operator, operatorPassword);
    expect((await createdChild).status()).toBe(201);
    await expect(createChild).toHaveCount(0);
    const delivery = operator.getByRole("dialog", { name: "Child agency login details", exact: true });
    await expect(delivery.getByLabel("Operator username", { exact: true })).toHaveValue("browser-child-operator");
    await expect(delivery.getByLabel("Temporary password", { exact: true })).not.toHaveValue("");
    await delivery.getByRole("button", { name: "Close", exact: true }).click();
    await expect(operator.getByRole("row").filter({ hasText: "Browser Child Agency" })).toBeVisible();

    await operator.getByRole("button", { name: "Customers", exact: true }).click();
    await operator.getByRole("row").filter({ hasText: "browser-managed" }).getByRole("button", { name: "Customer pricing", exact: true }).click();
    const customerPricing = operator.getByRole("dialog", { name: "Customer sales pricing · browser-managed", exact: true });
    await expect(customerPricing.getByRole("columnheader", { name: "Model name", exact: true })).toBeVisible();
    const customerModelPrice = customerPricing.getByLabel("Customer sales coefficient: browser-chat-model", { exact: true });
    await expect(customerModelPrice).toHaveAttribute("placeholder", "0.9500");
    await customerModelPrice.fill("0.97");
    await customerPricing.getByLabel("Change reason", { exact: true }).fill("Browser customer model price");
    const fixtureState = await (await operator.request.get("/__fixture/state")).json();
    const customerPublished = operator.waitForResponse((response) =>
      response.request().method() === "PUT" && response.url().endsWith(`/customers/${fixtureState.managed_user_id}/pricing/batch`),
    );
    await customerPricing.getByRole("button", { name: "Save customer pricing", exact: true }).click();
    await confirm(operator, operatorPassword);
    const customerResponse = await customerPublished;
    expect(customerResponse.status()).toBe(200);
    expect(customerResponse.request().postDataJSON()).toMatchObject({
      models: [{ model_name: "browser-chat-model", sales_bps: 9700 }],
    });
  } finally {
    await operatorContext.close();
  }
});

test("withdrawal uses a real account and exact decimal lease through review and confirmed payment", async ({
  page,
  browser,
}) => {
  await operatorLogin(page);
  await page.getByRole("button", { name: "Payout accounts", exact: true }).click();
  await page.getByRole("button", { name: "Add payout account", exact: true }).click();
  const accountDialog = page.getByRole("dialog", {
    name: "Add payout account",
    exact: true,
  });
  await accountDialog.getByLabel("Account holder", { exact: true }).fill("Browser Fixture Company");
  await accountDialog.getByLabel("Account number", { exact: true }).fill("6222000000001234");
  await accountDialog.getByLabel("Bank and branch", { exact: true }).fill("Fixture Bank");
  const accountResponse = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" && response.url().endsWith("/withdrawal-accounts"),
  );
  await accountDialog.getByRole("button", { name: "Save account version", exact: true }).click();
  await confirm(page, operatorPassword);
  expect((await accountResponse).status()).toBe(201);
  await expect(accountDialog).toHaveCount(0);
  await expect(page.getByRole("row").filter({ hasText: "1234" })).toBeVisible();
  await page.getByRole("button", { name: "Withdrawals", exact: true }).click();
  await page.getByRole("button", { name: "Request withdrawal", exact: true }).click();
  const withdrawalDialog = page.getByRole("dialog", {
    name: "Request withdrawal",
    exact: true,
  });
  await withdrawalDialog
    .getByRole("combobox", { name: "Payout account", exact: true })
    .selectOption({ index: 1 });
  await expect(withdrawalDialog.getByText("Chinese yuan (CNY)", { exact: true })).toBeVisible();
  await withdrawalDialog.getByLabel("Withdrawal amount", { exact: true }).fill("12.34");
  const created = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" && response.url().endsWith("/api/v1/withdrawals"),
  );
  await withdrawalDialog
    .getByRole("button", { name: "Confirm withdrawal request", exact: true })
    .click();
  await confirm(page, operatorPassword);
  const createdResponse = await created;
  expect(createdResponse.status()).toBe(201);
  expect(createdResponse.request().postDataJSON()).toMatchObject({
    amount_micros: "12340000",
    currency_code: "CNY",
    account_id: expect.any(String),
  });
  await expect(withdrawalDialog).toHaveCount(0);
  const rootContext = await browser.newContext({
    locale: "en-US",
    baseURL: test.info().project.use.baseURL,
  });
  const root = await rootContext.newPage();
  try {
    await rootLogin(root);
    await root.getByRole("button", { name: "Withdrawals", exact: true }).click();
    for (const action of [
      "Start review",
      "Approve withdrawal",
      "Record payment start",
      "Record unknown result",
      "Confirm unpaid and restore approval",
      "Record payment start",
    ]) {
      await root.getByRole("button", { name: action, exact: true }).click();
      const dialog = root.getByRole("dialog", { name: action, exact: true });
      await dialog
        .getByLabel("Reason and evidence", { exact: true })
        .fill("Isolated browser acceptance evidence");
      if (action === "Confirm unpaid and restore approval") {
        await dialog
          .getByLabel("Original bank reference", { exact: true })
          .fill("BROWSER-UNPAID-ATTEMPT");
        await dialog
          .getByLabel("Bank investigation reference", { exact: true })
          .fill("BROWSER-BANK-CASE-42");
        const localTime = await root.evaluate(() => {
          const now = new Date();
          const input = document.createElement("input");
          input.type = "datetime-local";
          input.step = "0.001";
          input.value = new Date(now.getTime() - now.getTimezoneOffset() * 60000)
            .toISOString()
            .slice(0, -1);
          // Browsers normalize .200 to .2 (and omit zero seconds). Fill the
          // canonical value so Playwright preserves the exact timestamp.
          return input.value;
        });
        await dialog.getByLabel("Bank confirmation time", { exact: true }).fill(localTime);
      }
      if (
        [
          "Record payment start",
          "Record unknown result",
          "Confirm unpaid and restore approval",
        ].includes(action)
      )
        await dialog.getByRole("checkbox").check();
      const response = root.waitForResponse(
        (result) =>
          result.request().method() === "POST" &&
          /\/root\/withdrawals\/\d+\/(review|transition)$/.test(result.url()),
      );
      await dialog.getByRole("button", { name: action, exact: true }).click();
      await confirm(root, rootPassword);
      expect((await response).status()).toBe(200);
      await expect(dialog).toHaveCount(0);
      if (action === "Confirm unpaid and restore approval") {
        const recovered = await (await root.request.get("/__fixture/state")).json();
        expect(recovered.withdrawals[0]).toMatchObject({
          status: "approved",
          payment_lease_token: "0",
        });
        expect(recovered.balances[0]).toMatchObject({
          AvailableMicros: 987660000,
          LockedMicros: 12340000,
          PaidMicros: 0,
        });
      }
    }
    const payingState = await (await root.request.get("/__fixture/state")).json();
    const lease = payingState.withdrawals[0].payment_lease_token;
    expect(BigInt(lease)).toBeGreaterThan(BigInt(Number.MAX_SAFE_INTEGER));
    // Losing browser memory must not lose the owning administrator's active lease.
    await root.reload();
    await root.getByRole("button", { name: "Withdrawals", exact: true }).click();
    await root.getByRole("button", { name: "Record confirmed payment", exact: true }).click();
    const paidDialog = root.getByRole("dialog", {
      name: "Record confirmed payment",
      exact: true,
    });
    await paidDialog
      .getByLabel("Original bank reference", { exact: true })
      .fill("BROWSER-FAKE-PAYMENT-001");
    await paidDialog.getByRole("checkbox").check();
    const paid = root.waitForResponse((response) =>
      /\/root\/withdrawals\/\d+\/mark-paid$/.test(response.url()),
    );
    await paidDialog.getByRole("button", { name: "Record confirmed payment", exact: true }).click();
    await confirm(root, rootPassword);
    const paidResponse = await paid;
    expect(paidResponse.request().postDataJSON().payment_lease_token).toBe(lease);
    expect(paidResponse.status()).toBe(200);
    await expect(paidDialog).toHaveCount(0);
    await expect(root.getByRole("table").getByText("Paid", { exact: true })).toBeVisible();
    const state = await (await root.request.get("/__fixture/state")).json();
    expect(state.withdrawals[0]).toMatchObject({
      status: "paid",
      amount_micros: "12340000",
      payment_reference: "BROWSER-FAKE-PAYMENT-001",
    });
    expect(state.balances[0]).toMatchObject({
      AvailableMicros: 987660000,
      LockedMicros: 0,
      PaidMicros: 12340000,
    });
    await root.getByLabel("Language", { exact: true }).selectOption("zh");
    await root.screenshot({
      path: test.info().outputPath("withdrawals-zh.png"),
      fullPage: true,
    });
  } finally {
    await rootContext.close();
  }
});

test("operator creates a scoped CSV and downloads the generated file without other agencies data", async ({
  page,
}) => {
  await operatorLogin(page);
  await page.getByRole("button", { name: "Data exports", exact: true }).click();
  const created = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" && response.url().endsWith("/api/v1/exports"),
  );
  await page.getByRole("button", { name: "Create CSV export", exact: true }).click();
  expect((await created).status()).toBe(202);
  const download = page.waitForEvent("download");
  await page.getByRole("button", { name: "Download CSV", exact: true }).click();
  const csv = await download;
  const destination = test.info().outputPath("agency-usage.csv");
  await csv.saveAs(destination);
  const bytes = await readFile(destination);
  expect([...bytes.subarray(0, 3)]).toEqual([239, 187, 191]);
  expect(bytes.toString("utf8")).toContain("browser-export-model");
  expect(bytes.toString("utf8")).not.toContain("OTHER-AGENCY-PRIVATE-MODEL");
  await page.getByLabel("Language", { exact: true }).selectOption("zh");
  await page.screenshot({
    path: test.info().outputPath("exports-zh.png"),
    fullPage: true,
  });
});

test("root can inspect and cancel a blocked binding, then transfer a managed customer with the observed revision", async ({
  page,
}) => {
  await rootLogin(page);
  const before = await (await page.request.get("/__fixture/state")).json();
  await page.getByRole("button", { name: "Customers", exact: true }).click();
  await page.getByRole("button", { name: "Customer assignment", exact: true }).click();
  let dialog = page.getByRole("dialog", {
    name: "Customer assignment",
    exact: true,
  });
  await dialog.getByLabel("Customer account", { exact: true }).fill(String(before.legacy_user_id));
  await dialog.getByRole("button", { name: "Look up customer", exact: true }).click();
  await dialog.getByLabel("Target agency ID", { exact: true }).fill(String(before.agency_id));
  await dialog.getByRole("button", { name: "Check target agency", exact: true }).click();
  await expect(dialog.getByText(/Browser Agency/)).toBeVisible();
  await dialog.getByLabel("Reason", { exact: true }).fill("Test isolated legacy binding");
  await dialog.getByRole("checkbox").check();
  const bound = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" && /\/root\/users\/\d+\/bind$/.test(response.url()),
  );
  await dialog.getByRole("button", { name: "Bind existing customer", exact: true }).click();
  await confirm(page, rootPassword);
  expect((await bound).status()).toBe(202);
  await expect(dialog.getByText("BROWSER-BINDING-BLOCKER", { exact: true })).toBeVisible();
  await dialog
    .getByLabel("Cancellation reason", { exact: true })
    .fill("Leave long-running task with its original user");
  const cancelled = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      /\/root\/provisioning\/\d+\/cancel$/.test(response.url()),
  );
  await dialog.getByRole("button", { name: "Cancel binding", exact: true }).click();
  await confirm(page, rootPassword);
  expect((await cancelled).status()).toBe(200);
  const afterCancel = await (await page.request.get("/__fixture/state")).json();
  expect(afterCancel).toMatchObject({
    legacy_billing_mode: "legacy",
    legacy_quota: 42,
  });
  await dialog.getByRole("button", { name: "Close", exact: true }).click();
  await page
    .getByRole("row")
    .filter({ hasText: "browser-managed" })
    .getByRole("button", { name: "Manage assignment", exact: true })
    .click();
  dialog = page.getByRole("dialog", {
    name: "Customer assignment",
    exact: true,
  });
  await dialog
    .getByLabel("Target agency ID", { exact: true })
    .fill(String(before.target_agency_id));
  await dialog.getByRole("button", { name: "Check target agency", exact: true }).click();
  await expect(dialog.getByText(/Transfer Target/)).toBeVisible();
  await dialog.getByLabel("Reason", { exact: true }).fill("Verified customer move");
  await dialog.getByRole("checkbox").check();
  const transferred = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      /\/root\/users\/\d+\/transfer$/.test(response.url()),
  );
  await dialog.getByRole("button", { name: "Transfer customer", exact: true }).click();
  await confirm(page, rootPassword);
  const transfer = await transferred;
  expect(transfer.status()).toBe(200);
  expect(transfer.request().postDataJSON()).toMatchObject({
    target_agency_id: String(before.target_agency_id),
    expected_binding_revision: "1",
  });
  const after = await (await page.request.get("/__fixture/state")).json();
  expect(
    after.bindings.find((row: { UserID: number }) => row.UserID === before.managed_user_id),
  ).toMatchObject({ AgencyID: before.target_agency_id, Revision: 2 });
});
