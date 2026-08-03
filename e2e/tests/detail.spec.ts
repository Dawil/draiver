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

    // The log timeline holds all three events of the attempt.
    await expect(page.getByTestId("event-1")).toBeVisible();
    await expect(page.getByTestId("event-2")).toContainText("Stripe test keys");
    const esc = page.getByTestId("event-3");
    await expect(esc).toContainText("Which currency rounding rule for JPY?");
    await expect(page.getByTestId("unresolved-3")).toBeVisible();

    // Default ordering is newest-first: escalation (#3) at the top, created (#1) at the bottom.
    const domOrder = () =>
      page
        .getByTestId("log-timeline")
        .locator("> li")
        .evaluateAll((els) => els.map((e) => e.getAttribute("data-testid")));
    expect(await domOrder()).toEqual(["event-3", "event-2", "event-1"]);
    await expect(page.getByTestId("log-order-toggle")).toHaveText("Newest first");
  });

  test("the Log toggle flips between newest-first and oldest-first", async ({ page }) => {
    await page.goto("/ticket/PROJ-101/0001");

    const timeline = page.getByTestId("log-timeline");
    const toggle = page.getByTestId("log-order-toggle");
    const domOrder = () =>
      timeline.locator("> li").evaluateAll((els) => els.map((e) => e.getAttribute("data-testid")));

    // Starts newest-first.
    expect(await domOrder()).toEqual(["event-3", "event-2", "event-1"]);
    await expect(toggle).toHaveText("Newest first");

    // One click → oldest-first.
    await toggle.click();
    expect(await domOrder()).toEqual(["event-1", "event-2", "event-3"]);
    await expect(toggle).toHaveText("Oldest first");
    await expect(toggle).toHaveAttribute("aria-pressed", "true");

    // Click again → back to newest-first.
    await toggle.click();
    expect(await domOrder()).toEqual(["event-3", "event-2", "event-1"]);
    await expect(toggle).toHaveText("Newest first");
    await expect(toggle).toHaveAttribute("aria-pressed", "false");
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
