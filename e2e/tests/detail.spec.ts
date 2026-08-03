import { test, expect } from "@playwright/test";

// Visual top-to-bottom order of the log, as the reader sees it. The DOM is
// always oldest-first; CSS (flex-direction on .log-region[data-order]) does the
// flip, so we sort by on-screen Y rather than by DOM position.
async function visualOrder(page: import("@playwright/test").Page) {
  return page
    .getByTestId("log-timeline")
    .locator("> li")
    .evaluateAll((els) =>
      els
        .map((e) => ({
          id: e.getAttribute("data-testid") || "",
          y: e.getBoundingClientRect().top,
        }))
        .sort((a, b) => a.y - b.y)
        .map((e) => e.id),
    );
}

async function domOrder(page: import("@playwright/test").Page) {
  return page
    .getByTestId("log-timeline")
    .locator("> li")
    .evaluateAll((els) => els.map((e) => e.getAttribute("data-testid")));
}

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

    // The DOM is oldest-first (a stable order htmx swaps can't disturb)...
    expect(await domOrder(page)).toEqual(["event-1", "event-2", "event-3"]);
    // ...while the default VISUAL order is newest-first: escalation (#3) on top,
    // created (#1) at the bottom, achieved by CSS on the region.
    expect(await visualOrder(page)).toEqual(["event-3", "event-2", "event-1"]);
    await expect(page.getByTestId("log-region")).toHaveAttribute("data-order", "newest");
    await expect(page.getByTestId("log-order-toggle")).toHaveText("Newest first");
  });

  test("the Log toggle flips the visual order between newest-first and oldest-first", async ({ page }) => {
    await page.goto("/ticket/PROJ-101/0001");

    const region = page.getByTestId("log-region");
    const toggle = page.getByTestId("log-order-toggle");

    // Starts newest-first (visually), oldest-first in the DOM.
    expect(await domOrder(page)).toEqual(["event-1", "event-2", "event-3"]);
    expect(await visualOrder(page)).toEqual(["event-3", "event-2", "event-1"]);
    await expect(region).toHaveAttribute("data-order", "newest");
    await expect(toggle).toHaveText("Newest first");

    // One click → oldest-first VISUALLY; the DOM never reorders.
    await toggle.click();
    expect(await domOrder(page)).toEqual(["event-1", "event-2", "event-3"]);
    expect(await visualOrder(page)).toEqual(["event-1", "event-2", "event-3"]);
    await expect(region).toHaveAttribute("data-order", "oldest");
    await expect(toggle).toHaveText("Oldest first");
    await expect(toggle).toHaveAttribute("aria-pressed", "true");

    // Click again → back to newest-first.
    await toggle.click();
    expect(await visualOrder(page)).toEqual(["event-3", "event-2", "event-1"]);
    await expect(region).toHaveAttribute("data-order", "newest");
    await expect(toggle).toHaveText("Newest first");
    await expect(toggle).toHaveAttribute("aria-pressed", "false");
  });

  test("the log region polls a GET-only live fragment that updates the badge out-of-band", async ({ page }) => {
    await page.goto("/ticket/PROJ-101/0001");

    // Polling is armed on the log region and is GET-only (read-only invariant).
    const region = page.getByTestId("log-region");
    await expect(region).toHaveAttribute("hx-get", "/ticket/PROJ-101/0001/live");
    await expect(region).toHaveAttribute("hx-trigger", "every 3s");

    // The fragment itself: log <ol> as the primary swap, state badge OOB, no
    // full-page chrome — and it is reachable by GET.
    const res = await page.request.get("/ticket/PROJ-101/0001/live");
    expect(res.status()).toBe(200);
    const html = await res.text();
    expect(html).toContain('data-testid="log-timeline"');
    expect(html).toContain('id="state-badge"');
    expect(html).toContain('hx-swap-oob="true"');
    expect(html).not.toContain("<html");
  });

  test("the reader's chosen order survives a live poll", async ({ page }) => {
    await page.goto("/ticket/PROJ-101/0001");
    const toggle = page.getByTestId("log-order-toggle");

    // Flip to oldest-first, then let at least one 3s poll cycle land.
    await toggle.click();
    await expect(page.getByTestId("log-region")).toHaveAttribute("data-order", "oldest");
    await page.waitForTimeout(3500);

    // The swap replaced the <ol> but left the region's data-order untouched, so
    // the chosen order is preserved.
    await expect(page.getByTestId("log-region")).toHaveAttribute("data-order", "oldest");
    await expect(toggle).toHaveText("Oldest first");
    expect(await visualOrder(page)).toEqual(["event-1", "event-2", "event-3"]);
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
