import { expect, test, type Page } from "@playwright/test";

const rootPassword = "Browser-root-2026!";
const operatorPassword = "Browser-operator-2026!";

async function rootLogin(page: Page) {
  const response = await page.request.post("/__fixture/root");
  expect(response.ok()).toBeTruthy();
  await page.goto("/agency/");
  await expect(
    page.getByRole("button", { name: "Sign out", exact: true }),
  ).toBeVisible();
}

async function operatorLogin(page: Page) {
  await page.goto("/agency/");
  await page.getByLabel("Username", { exact: true }).fill("browser-operator");
  await page.getByLabel("Password", { exact: true }).fill(operatorPassword);
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Sign out", exact: true }),
  ).toBeVisible();
}

async function confirmRoot(page: Page) {
  const dialog = page.getByRole("dialog", {
    name: "Confirm this action",
    exact: true,
  });
  await expect(dialog).toBeVisible();
  await dialog
    .getByLabel("Current password", { exact: true })
    .fill(rootPassword);
  await dialog
    .getByRole("button", { name: "Verify and continue", exact: true })
    .click();
}

function issueRow(page: Page, id: string) {
  return page
    .getByRole("row")
    .filter({
      has: page.getByRole("cell", { name: id, exact: true }),
    })
    .filter({
      has: page.getByRole("button", {
        name: /Inspect evidence|查看证据/,
        exact: true,
      }),
    });
}

test("root reviews reconciliation evidence and safely restores one missing delivery", async ({
  page,
  browser,
}) => {
  const seedResponse = await page.request.post("/__fixture/reconciliation");
  expect(seedResponse.status()).toBe(201);
  const seeded = await seedResponse.json();
  expect(seeded).toMatchObject({
    funding_issue_id: expect.any(String),
    delivery_issue_id: expect.any(String),
    unsupported_issue_id: expect.any(String),
    event_id: "browser-reconciliation-missing-delivery",
  });

  await rootLogin(page);
  await page
    .getByRole("button", { name: "Sync & reconciliation", exact: true })
    .click();
  await expect(issueRow(page, seeded.funding_issue_id)).toBeVisible();
  await expect(issueRow(page, seeded.delivery_issue_id)).toBeVisible();
  await expect(issueRow(page, seeded.unsupported_issue_id)).toBeVisible();

  const fundingRow = issueRow(page, seeded.funding_issue_id);
  await fundingRow
    .getByRole("button", { name: "Inspect evidence", exact: true })
    .click();
  let review = page.getByRole("dialog", {
    name: "Review reconciliation issue",
    exact: true,
  });
  await expect(review).toBeVisible();
  await expect(review).toContainText("wallet quota 100, available funding 90");
  await expect(review).toContainText("Mismatch");
  await expect(
    review
      .getByRole("row")
      .filter({ has: page.getByRole("cell", { name: "Wallet and funding balance", exact: true }) }),
  ).toContainText("90100Mismatch");
  await expect(review.getByLabel("Treatment", { exact: true })).toHaveCount(0);
  await expect(
    review.getByRole("button", {
      name: "Confirm verified resolution",
      exact: true,
    }),
  ).toHaveCount(0);
  await expect(
    review.getByRole("button", {
      name: "Restore missing delivery",
      exact: true,
    }),
  ).toHaveCount(0);
  await review.getByRole("button", { name: "Close", exact: true }).click();

  const unsupportedRow = issueRow(page, seeded.unsupported_issue_id);
  await unsupportedRow
    .getByRole("button", { name: "Inspect evidence", exact: true })
    .click();
  review = page.getByRole("dialog", {
    name: "Review reconciliation issue",
    exact: true,
  });
  await expect(review).toContainText("cannot prove a safe resolution");
  await expect(review.getByLabel("Treatment", { exact: true })).toHaveCount(0);
  await expect(
    review.getByRole("button", {
      name: "Confirm verified resolution",
      exact: true,
    }),
  ).toHaveCount(0);
  await expect(
    review.getByRole("button", {
      name: "Restore missing delivery",
      exact: true,
    }),
  ).toHaveCount(0);
  await review.getByRole("button", { name: "Close", exact: true }).click();

  const before = await (
    await page.request.get("/__fixture/reconciliation-state")
  ).json();
  expect(before.deliveries).toEqual([]);
  expect(before.wallet_quota).toBe(100);
  expect(before.paid_available).toBe(90);
  expect(before.money_seq).toBe(1);

  const deliveryRow = issueRow(page, seeded.delivery_issue_id);
  await deliveryRow
    .getByRole("button", { name: "Inspect evidence", exact: true })
    .click();
  review = page.getByRole("dialog", {
    name: "Review reconciliation issue",
    exact: true,
  });
  await expect(review).toContainText("immutable event has no delivery");
  await expect(review).toContainText("Restore only the missing delivery");
  const inspected = await (
    await page.request.get(
      `/agency/api/v1/root/reconciliation/issues/${seeded.delivery_issue_id}`,
    )
  ).json();
  const note =
    "Verified the immutable top-up event and restored its missing delivery exactly once.";
  const unverifiedStatus = await page.evaluate(
    async ({ id, hash, resolution }) => {
      const csrf = decodeURIComponent(
        document.cookie
          .split("; ")
          .find((cookie) => cookie.startsWith("agency_csrf="))
          ?.split("=")
          .slice(1)
          .join("=") || "",
      );
      const response = await fetch(
        `/agency/api/v1/root/reconciliation/issues/${id}/resolve`,
        {
          method: "POST",
          headers: {
            "Content-Type": "application/json",
            "X-CSRF-Token": csrf,
            "Idempotency-Key": crypto.randomUUID(),
          },
          body: JSON.stringify({
            action: "restore_delivery",
            status: "resolved",
            expected_evidence_hash: hash,
            resolution,
          }),
        },
      );
      return response.status;
    },
    {
      id: seeded.delivery_issue_id,
      hash: inspected.data.verification.evidence_hash,
      resolution: note,
    },
  );
  expect(unverifiedStatus).toBe(403);
  expect(
    (await (await page.request.get("/__fixture/reconciliation-state")).json())
      .deliveries,
  ).toEqual([]);
  await expect(
    review.getByRole("button", {
      name: "Restore missing delivery",
      exact: true,
    }),
  ).toBeDisabled();
  await review.getByLabel("Resolution note", { exact: true }).fill(note);
  await review
    .getByLabel("I reviewed the evidence and this action.", { exact: true })
    .check();
  const resolveResponse = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      response
        .url()
        .endsWith(
          `/root/reconciliation/issues/${seeded.delivery_issue_id}/resolve`,
        ),
  );
  await review
    .getByRole("button", { name: "Restore missing delivery", exact: true })
    .click();
  await confirmRoot(page);
  const resolved = await resolveResponse;
  expect(resolved.status()).toBe(200);
  expect(resolved.request().postDataJSON()).toMatchObject({
    action: "restore_delivery",
    status: "resolved",
    expected_evidence_hash: inspected.data.verification.evidence_hash,
  });
  await expect(review).toContainText("Recorded resolution");
  await expect(review).toContainText(
    "restored its missing delivery exactly once",
  );

  const after = await (
    await page.request.get("/__fixture/reconciliation-state")
  ).json();
  expect(after.wallet_quota).toBe(before.wallet_quota);
  expect(after.paid_available).toBe(before.paid_available);
  expect(after.money_seq).toBe(before.money_seq);
  expect(after.payload_hash).toBe(before.payload_hash);
  expect(after.payload).toBe(before.payload);
  expect(after.deliveries).toHaveLength(1);
  expect(after.deliveries[0]).toMatchObject({
    EventID: seeded.event_id,
    Status: "pending",
    Attempts: 0,
  });
  expect(after.issues[seeded.delivery_issue_id]).toMatchObject({
    status: "resolved",
    resolution_evidence: expect.any(String),
    repair_event_id: expect.any(String),
  });
  const persistedEvidence = JSON.parse(
    after.issues[seeded.delivery_issue_id].resolution_evidence,
  );
  expect(persistedEvidence).toMatchObject({
    action: "restore_delivery",
    before: { state: "inconsistent" },
    after: { state: "consistent" },
  });
  expect(after.issues[seeded.delivery_issue_id].repair_event_id).not.toBe("");
  expect(after.issues[seeded.funding_issue_id].status).toBe("open");
  expect(after.issues[seeded.unsupported_issue_id].status).toBe("open");
  expect(after.audits).toEqual(
    expect.arrayContaining([
      expect.objectContaining({
        Action: "reconciliation.resolve",
        ObjectType: "reconciliation_issue",
        ObjectID: seeded.delivery_issue_id,
      }),
    ]),
  );

  await review.getByRole("button", { name: "Close", exact: true }).click();
  const beforeRun = await (await page.request.get("/__fixture/state")).json();
  const runResponse = page.waitForResponse(
    (response) => response.request().method() === "POST" && response.url().endsWith("/root/reconciliation/runs"),
  );
  const runProof = page.waitForResponse(
    (response) => response.request().method() === "POST" && response.url().endsWith("/__fixture/proof"),
  );
  await page.getByRole("button", { name: "Run reconciliation", exact: true }).click();
  await confirmRoot(page);
  expect((await runProof).request().postDataJSON()).toMatchObject({
    action: "reconciliation.run",
    object_id: "reconciliation:run",
  });
  const completedRun = await runResponse;
  expect(completedRun.status()).toBe(201);
  const runKey = completedRun.request().headers()["idempotency-key"];
  const runHistory = await (await page.request.get("/agency/api/v1/root/reconciliation/runs")).json();
  const recordedRun = runHistory.data.items.find((run: { run_key: string }) => run.run_key === runKey);
  expect(recordedRun).toMatchObject({ id: expect.any(String), trigger: "manual", status: "completed", error: "" });
  expect(BigInt(recordedRun.finished_at_ms)).toBeGreaterThanOrEqual(BigInt(recordedRun.started_at_ms));
  const history = page.locator("section").filter({
    has: page.getByRole("heading", { name: "Reconciliation history", exact: true }),
  }).last();
  await expect(history.getByRole("row").filter({
    has: page.getByRole("cell", { name: recordedRun.id, exact: true }),
  })).toContainText("completed");
  const afterRun = await (await page.request.get("/__fixture/reconciliation-state")).json();
  expect(afterRun.wallet_quota).toBe(after.wallet_quota);
  expect(afterRun.paid_available).toBe(after.paid_available);
  expect(afterRun.money_seq).toBe(after.money_seq);
  expect(afterRun.payload).toBe(after.payload);
  expect(afterRun.payload_hash).toBe(after.payload_hash);
  expect(afterRun.deliveries).toEqual(after.deliveries);
  for (const issueID of [seeded.funding_issue_id, seeded.delivery_issue_id, seeded.unsupported_issue_id])
    expect(afterRun.issues[issueID]).toEqual(after.issues[issueID]);
  const globalAfterRun = await (await page.request.get("/__fixture/state")).json();
  expect(globalAfterRun.balances).toEqual(beforeRun.balances);
  expect(globalAfterRun.withdrawals).toEqual(beforeRun.withdrawals);
  await page.reload();
  await page
    .getByRole("button", { name: "Sync & reconciliation", exact: true })
    .click();
  await expect(history.getByRole("row").filter({
    has: page.getByRole("cell", { name: recordedRun.id, exact: true }),
  })).toContainText("completed");
  await issueRow(page, seeded.delivery_issue_id)
    .getByRole("button", {
      name: "Inspect evidence",
      exact: true,
    })
    .click();
  review = page.getByRole("dialog", {
    name: "Review reconciliation issue",
    exact: true,
  });
  await expect(review).toContainText("Recorded resolution");
  await expect(review).toContainText(
    "restored its missing delivery exactly once",
  );
  const reloaded = await (
    await page.request.get(
      `/agency/api/v1/root/reconciliation/issues/${seeded.delivery_issue_id}`,
    )
  ).json();
  expect(reloaded.data.issue.resolution_evidence).toBe(
    after.issues[seeded.delivery_issue_id].resolution_evidence,
  );
  await expect(review).toContainText(
    after.issues[seeded.delivery_issue_id].repair_event_id,
  );
  await expect(
    review.getByRole("button", {
      name: "Restore missing delivery",
      exact: true,
    }),
  ).toHaveCount(0);
  await review.getByRole("button", { name: "Close", exact: true }).click();
  await page.getByRole("button", { name: "Audit log", exact: true }).click();
  await expect(
    page.getByRole("row").filter({ hasText: "reconciliation.resolve" }),
  ).toContainText(seeded.delivery_issue_id);
  await page
    .getByRole("button", { name: "Sync & reconciliation", exact: true })
    .click();
  await page.getByLabel("Language", { exact: true }).selectOption("zh");
  await issueRow(page, seeded.delivery_issue_id)
    .getByRole("button", { name: "查看证据", exact: true })
    .click();
  const chineseReview = page.getByRole("dialog", {
    name: "复核对账异常",
    exact: true,
  });
  await expect(chineseReview).toContainText("已记录的处理结果");
  await page.screenshot({
    path: test.info().outputPath("reconciliation-evidence-zh.png"),
  });
  await chineseReview
    .getByRole("heading", { name: "已记录的处理结果", exact: true })
    .scrollIntoViewIfNeeded();
  await page.screenshot({
    path: test.info().outputPath("reconciliation-resolution-zh.png"),
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await chineseReview.evaluate((dialog) => { dialog.scrollTop = 0; });
  expect(await chineseReview.evaluate((dialog) => {
    const rect = dialog.getBoundingClientRect();
    return rect.left >= 0 && rect.right <= window.innerWidth;
  })).toBe(true);
  await page.screenshot({ path: test.info().outputPath("reconciliation-mobile-zh.png") });

  const operatorContext = await browser.newContext({
    locale: "en-US",
    baseURL: test.info().project.use.baseURL,
  });
  const operator = await operatorContext.newPage();
  try {
    await operatorLogin(operator);
    await expect(
      operator.getByRole("button", {
        name: "Sync & reconciliation",
        exact: true,
      }),
    ).toHaveCount(0);
    expect(
      (
        await operator.request.get("/agency/api/v1/root/reconciliation/issues")
      ).status(),
    ).toBe(403);
    expect(
      (
        await operator.request.get(
          `/agency/api/v1/root/reconciliation/issues/${seeded.delivery_issue_id}`,
        )
      ).status(),
    ).toBe(403);
  } finally {
    await operatorContext.close();
  }
});
