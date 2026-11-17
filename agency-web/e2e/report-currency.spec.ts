import { expect, test } from "@playwright/test";

for (const currency of ["USD", "CNY"] as const) {
  test(`${currency} customer reports use regional quota amounts and preserve receipt currency`, async ({ page }) => {
    const usd = currency === "USD";
    await page.route("**/agency/", async (route) => {
      const response = await route.fetch();
      const html = await response.text();
      const config = {
        base_path: "/agency", platform_base_url: "",
        quota_display: { currency_code: currency, currency_symbol: usd ? "$" : "¥", quota_per_unit: 500000, usd_to_currency_rate: usd ? 1 : 7.3 },
      };
      await route.fulfill({ response, body: html.replace("</head>", `<script>window.__AGENCY_CONFIG__=${JSON.stringify(config)}</script></head>`) });
    });
    await page.route("**/agency/api/v1/**", async (route) => {
      const path = new URL(route.request().url()).pathname.replace("/agency/api/v1", "");
      let data: unknown = { items: [], total: 0 };
      if (path === "/auth/me") data = { actor_type: "root", actor_id: 1, agency_id: 7, acting_agency: { display_name: "Currency agency", operator_username: "currency-operator" }, must_change_password: false };
      if (path === "/customers") data = { items: [{ user_id: "17", username: "currency-customer", credit_configured: true, credit_limit: 50000000, credit_used_quota: 500000, credit_available_quota: 49500000 }], total: 1 };
      if (path === "/customers/17/usage") data = { items: [{ event_id: "currency-usage", model: "currency-model", currency_code: currency, standard_quota: "958904", charged_quota: "239726", paid_quota: "0", nonpaid_quota: "0", debt_quota: "239726", funding_breakdown: [{ source: "debt", quota: "239726" }] }], total: 1 };
      if (path === "/customers/17/topups") data = { items: [
        { currency_code: "USD", actual_money: "100", credited_quota: "50000000", paid_quota: "50000000", bonus_quota: "0", consumed_quota: "500000", remaining_quota: "49500000", expired_quota: "0", refunded_quota: "0" },
        { currency_code: "CNY", actual_money: "73", credited_quota: "5000000", paid_quota: "5000000", bonus_quota: "0", consumed_quota: "0", remaining_quota: "5000000", expired_quota: "0", refunded_quota: "0" },
      ], total: 2 };
      await route.fulfill({ json: { success: true, data } });
    });
    await page.goto("/agency/");
    await page.getByRole("button", { name: "Customers", exact: true }).click();
    const customer = page.getByRole("row").filter({ hasText: "currency-customer" });
    await expect(customer).toContainText(usd ? "$100.00" : "¥730.00");
    await customer.getByRole("button", { name: "View usage and top-ups" }).click();
    const usage = page.getByRole("row").filter({ hasText: "currency-model" });
    await expect(usage.getByRole("cell").nth(8)).toHaveText(usd ? "$1.92" : "¥14.00");
    await expect(usage.getByRole("cell").nth(10)).toHaveText(usd ? "$0.48" : "¥3.50");
    await expect(usage.getByRole("cell").nth(14)).toContainText(usd ? "$0.48" : "¥3.50");
    await page.getByRole("button", { name: "Top-ups", exact: true }).click();
    const rows = page.getByRole("row");
    await expect(rows.nth(1).getByRole("cell").nth(3)).toHaveText("$100.00");
    await expect(rows.nth(1).getByRole("cell").nth(4)).toHaveText("USD");
    await expect(rows.nth(1).getByRole("cell").nth(5)).toHaveText(usd ? "$100.00" : "¥730.00");
    await expect(rows.nth(2).getByRole("cell").nth(3)).toHaveText("¥73.00");
    await expect(rows.nth(2).getByRole("cell").nth(4)).toHaveText("CNY");
    await expect(rows.nth(2).getByRole("cell").nth(5)).toHaveText(usd ? "$10.00" : "¥73.00");
  });
}
