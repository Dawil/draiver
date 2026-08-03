import { test, expect } from "@playwright/test";

test.describe("attempt detail", () => {
  test("opens from the board and renders spec + log timeline", async ({ page }) => {
    await page.goto("/");
    await page.getByTestId("col-stuck").getByTestId("attempt-link-PROJ-101-0001").click();

    await expect(page).toHaveURL(/\/ticket\/PROJ-101\/0001$/);
    await expect(page.getByTestId("ticket-detail")).toBeVisible();
    await expect(page.getByTestId("state-badge")).toHaveText("Stuck");
    // Attempt provenance is shown.
    await expect(page.getByTestId("attempt-tool")).toHaveText("claude-code");

    // Spec markdown is rendered (the scaffold's H1 carries the title).
    await expect(page.getByTestId("spec").getByRole("heading", { name: "Payment webhook" })).toBeVisible();

    // The log timeline: created, gotcha, escalation, in order within the attempt.
    await expect(page.getByTestId("event-1")).toBeVisible();
    await expect(page.getByTestId("event-2")).toContainText("Stripe test keys");
    const esc = page.getByTestId("event-3");
    await expect(esc).toContainText("Which currency rounding rule for JPY?");
    await expect(page.getByTestId("unresolved-3")).toBeVisible();
  });

  test("the attempt index lists a ticket's attempts", async ({ page }) => {
    await page.goto("/ticket/PROJ-101");
    await expect(page.getByTestId("attempt-index")).toBeVisible();
    await expect(page.getByTestId("attempt-link-PROJ-101-0001")).toBeVisible();
    await expect(page.getByTestId("attempt-link-PROJ-101-0002")).toBeVisible();
  });

  test("unknown ticket and unknown attempt 404", async ({ page }) => {
    expect((await page.request.get("/ticket/NOPE-1")).status()).toBe(404);
    expect((await page.request.get("/ticket/PROJ-101/9999")).status()).toBe(404);
  });
});
