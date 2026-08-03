import { test, expect } from "@playwright/test";

test.describe("ticket detail", () => {
  test("opens from the board and renders spec + log timeline", async ({ page }) => {
    await page.goto("/");
    await page.getByTestId("col-needs-me").getByTestId("ticket-link-PROJ-101").click();

    await expect(page).toHaveURL(/\/ticket\/PROJ-101$/);
    await expect(page.getByTestId("ticket-detail")).toBeVisible();
    await expect(page.getByTestId("state-badge")).toHaveText("Needs me");

    // Spec markdown is rendered (the scaffold's H1 carries the title).
    await expect(page.getByTestId("spec").getByRole("heading", { name: "Payment webhook" })).toBeVisible();

    // The log timeline shows the created, gotcha, and escalation events in order.
    await expect(page.getByTestId("event-1")).toBeVisible();
    await expect(page.getByTestId("event-2")).toContainText("Stripe test keys");
    const esc = page.getByTestId("event-3");
    await expect(esc).toContainText("Which currency rounding rule for JPY?");
    // The escalation is flagged as still needing a human.
    await expect(page.getByTestId("unresolved-3")).toBeVisible();
  });

  test("unknown ticket 404s", async ({ page }) => {
    const res = await page.request.get("/ticket/NOPE-1");
    expect(res.status()).toBe(404);
  });
});
