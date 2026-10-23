import { test, expect } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { readdirSync, rmSync } from "node:fs";
import path from "node:path";

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

    // A Stuck card deep-links to its open escalation (the latest event, #3).
    await expect(page).toHaveURL(/\/ticket\/PROJ-101\/0001#event-3$/);
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

  // drvweb-025 (AC 2): the live log region self-polls every 3s. The morph swap
  // makes zero DOM mutations when nothing changed, so a text selection a reader
  // holds in a log entry survives the tick and stays copyable.
  test("a text selection in the live log survives a 3s poll", async ({ page }) => {
    await page.goto("/ticket/PROJ-101/0001");
    const entry = page.getByTestId("event-2");
    await expect(entry).toContainText("Stripe test keys");

    // Select the entry's body text — the stable prose, not the relative-time
    // stamp, which the server legitimately re-renders ("just now" → "Ns ago").
    const body = entry.locator(".event-body");
    const selected = await body.evaluate((el) => {
      const r = document.createRange();
      r.selectNodeContents(el);
      const s = window.getSelection()!;
      s.removeAllRanges();
      s.addRange(r);
      return s.toString();
    });
    expect(selected).toContain("Stripe test keys");

    await page.waitForTimeout(3600); // at least one poll cycle

    const after = await page.evaluate(() => window.getSelection()?.toString() ?? "");
    expect(after).toBe(selected);
  });

  // drvweb-025 (AC 3): when the log DOES change, morph mutates only the changed
  // nodes — a new entry is inserted while a selection held on an untouched entry
  // survives. The 204/innerHTML paths could not do this; morph can. We append a
  // real event via the CLI (maintaining the hash-chained log) and restore it after.
  test("a new log line morphs in while a selection on an unchanged entry survives", async ({ page }) => {
    const fixtureDir = path.join(__dirname, "..", ".fixture-data");
    const bin = path.join(__dirname, "..", ".bin", "draiver");
    const logDir = path.join(fixtureDir, "PROJ-101", "attempts", "0001", "log");
    const before = new Set(readdirSync(logDir));

    try {
      await page.goto("/ticket/PROJ-101/0001");
      const entry = page.getByTestId("event-2");
      await expect(entry).toContainText("Stripe test keys");

      // Hold a selection on event-2's body (which the append will not touch).
      const body = entry.locator(".event-body");
      const selected = await body.evaluate((el) => {
        const r = document.createRange();
        r.selectNodeContents(el);
        const s = window.getSelection()!;
        s.removeAllRanges();
        s.addRange(r);
        return s.toString();
      });
      expect(selected).toContain("Stripe test keys");

      // Append a 4th event out of band; the next poll should morph it in.
      execFileSync(
        bin,
        ["--data", fixtureDir, "log", "PROJ-101@0001", "a freshly appended note", "--type", "note"],
        { stdio: "pipe", env: { ...process.env, DRAIVER_ACTOR: "agent:claude-code" } },
      );

      // The new entry appears in place (morphed in, not a full re-render)...
      await expect(page.getByTestId("event-4")).toContainText("a freshly appended note");
      // ...and the selection on the untouched entry is still intact.
      const after = await page.evaluate(() => window.getSelection()?.toString() ?? "");
      expect(after).toBe(selected);
    } finally {
      // Restore the 3-event fixture for the other specs.
      for (const f of readdirSync(logDir)) {
        if (!before.has(f)) rmSync(path.join(logDir, f));
      }
    }
  });

  // task-011: a single log entry is linkable, lands on load, and stays
  // highlighted through the order toggle and the periodic log swap. drvweb-025:
  // the swap is now a morph (idiomorph), which reuses the <li id="event-N"> node
  // in place, so the browser's native :target reference survives on its own — the
  // old .is-target JS mirror is gone, and highlighting is pure CSS `.event:target`.
  test.describe("deep link to a log entry", () => {
    // How many .event elements currently match :target (a dynamic pseudo-class
    // Playwright's selector engine can't query, so evaluate it in the page).
    const targetedCount = (page: import("@playwright/test").Page) =>
      page.evaluate(() => document.querySelectorAll(".event:target").length);
    const isTargeted = (page: import("@playwright/test").Page, testid: string) =>
      page.evaluate(
        (tid) => document.querySelector(`[data-testid="${tid}"]`)?.matches(":target") ?? false,
        testid,
      );

    test("lands on and highlights the targeted entry on load", async ({ page }) => {
      await page.goto("/ticket/PROJ-101/0001#event-2");
      const target = page.getByTestId("event-2");
      await expect.poll(() => isTargeted(page, "event-2")).toBe(true);
      await expect(target).toBeInViewport();
      // Only the targeted entry is highlighted.
      await expect.poll(() => targetedCount(page)).toBe(1);
    });

    test("the seq label is a copyable permalink that highlights on click", async ({ page }) => {
      await page.goto("/ticket/PROJ-101/0001");
      const link = page.getByTestId("event-2").locator("a.seq");
      await expect(link).toHaveAttribute("href", "#event-2");
      await link.click();
      await expect(page).toHaveURL(/\/ticket\/PROJ-101\/0001#event-2$/);
      await expect.poll(() => isTargeted(page, "event-2")).toBe(true);
    });

    test("the highlight survives the order toggle (DOM ids never move)", async ({ page }) => {
      await page.goto("/ticket/PROJ-101/0001#event-1");
      await expect.poll(() => isTargeted(page, "event-1")).toBe(true);
      await page.getByTestId("log-order-toggle").click();
      await expect(page.getByTestId("log-region")).toHaveAttribute("data-order", "oldest");
      await expect.poll(() => isTargeted(page, "event-1")).toBe(true);
    });

    test("the highlight survives a live poll", async ({ page }) => {
      await page.goto("/ticket/PROJ-101/0001#event-2");
      await expect.poll(() => isTargeted(page, "event-2")).toBe(true);
      // Let at least one 3s poll morph the log region. The morph reuses the
      // <li id="event-2"> node by id, so native :target stays attached — the
      // highlight persists with no JS mirror.
      await page.waitForTimeout(3500);
      await expect.poll(() => isTargeted(page, "event-2")).toBe(true);
      await expect.poll(() => targetedCount(page)).toBe(1);
    });

    test("a stale/unknown seq neither errors nor jumps", async ({ page }) => {
      const errors: string[] = [];
      page.on("pageerror", (e) => errors.push(String(e)));
      await page.goto("/ticket/PROJ-101/0001#event-999");
      await expect(page.getByTestId("ticket-detail")).toBeVisible();
      await expect.poll(() => targetedCount(page)).toBe(0);
      expect(errors).toEqual([]);
    });
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
