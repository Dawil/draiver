import { test, expect } from "@playwright/test";

// drvweb-025: deep-link highlighting is now pure CSS `.event:target`, kept alive
// across the morph poll because the targeted node is reused in place — so assert
// the native :target match rather than the retired `.is-target` JS mirror.
const isTargeted = (page: import("@playwright/test").Page, testid: string) =>
  page.evaluate(
    (tid) => document.querySelector(`[data-testid="${tid}"]`)?.matches(":target") ?? false,
    testid,
  );

test.describe("board control states (per attempt)", () => {
  test("each attempt lands in its own control state", async ({ page }) => {
    await page.goto("/");

    // Counts are per attempt, weighted toward what needs the human.
    await expect(page.getByTestId("count-stuck")).toHaveText("1");
    await expect(page.getByTestId("count-review")).toHaveText("1");
    // PROJ-101/0002 (aider) and PROJ-103/0001 are both Running.
    await expect(page.getByTestId("count-running")).toHaveText("2");

    // The blocked attempt is on the board (Stuck).
    const stuck = page.getByTestId("col-stuck");
    await expect(stuck.getByTestId("attempt-link-PROJ-101-0001")).toBeVisible();
    await expect(stuck.getByText("1 open")).toBeVisible();

    // The claimed attempt awaits review.
    await expect(page.getByTestId("col-review").getByTestId("attempt-link-PROJ-102-0001")).toBeVisible();

    // The same ticket appears again as a second Running card (attempt 0002).
    const running = page.getByTestId("col-running");
    await expect(running.getByTestId("attempt-link-PROJ-101-0002")).toBeVisible();
    await expect(running.getByTestId("attempt-link-PROJ-103-0001")).toBeVisible();
  });

  // task-011 (board follow-through): a Stuck/Review card deep-links to the log
  // entry that put it there, so one click lands the human on the escalation or
  // review claim already highlighted — no scrolling the log to find it.
  test("a Stuck card opens on and highlights its open escalation", async ({ page }) => {
    await page.goto("/");
    await page.getByTestId("col-stuck").getByTestId("attempt-link-PROJ-101-0001").click();

    // PROJ-101/0001's latest event is the escalation (#3).
    await expect(page).toHaveURL(/\/ticket\/PROJ-101\/0001#event-3$/);
    const esc = page.getByTestId("event-3");
    await expect.poll(() => isTargeted(page, "event-3")).toBe(true);
    await expect(esc).toContainText("Which currency rounding rule for JPY?");
    await expect(esc).toBeInViewport();
  });

  test("a Review card opens on and highlights its review claim", async ({ page }) => {
    await page.goto("/");
    await page.getByTestId("col-review").getByTestId("attempt-link-PROJ-102-0001").click();

    // PROJ-102/0001's latest event is the review claim (#3).
    await expect(page).toHaveURL(/\/ticket\/PROJ-102\/0001#event-3$/);
    const review = page.getByTestId("event-3");
    await expect.poll(() => isTargeted(page, "event-3")).toBe(true);
    await expect(review).toContainText("PR #142 open");
  });

  test("a Running card links to the attempt with no deep-link fragment", async ({ page }) => {
    await page.goto("/");
    const link = page.getByTestId("col-running").getByTestId("attempt-link-PROJ-103-0001");
    await expect(link).toHaveAttribute("href", "/ticket/PROJ-103/0001");
  });

  // drvweb-015: the Pending tier — a desired, log-Running attempt with no live
  // agent, held out of admission by its after: gate. It renders as a quiet count
  // nested under Running (decision #2), with the "waiting on X" reason on the card,
  // and does not inflate the Running count.
  test("a Pending attempt shows in the Pending tier with its waiting reason", async ({ page }) => {
    await page.goto("/");

    const pending = page.getByTestId("col-pending");
    await expect(page.getByTestId("count-pending")).toHaveText("1");
    await expect(pending.getByTestId("attempt-link-PROJ-104-0001")).toBeVisible();
    await expect(pending.getByTestId("waiting-PROJ-104-0001")).toHaveText("waiting on PROJ-103");

    // The Pending tier lives inside the Running column's vertical (decision #2)...
    await expect(page.getByTestId("col-running").getByTestId("col-pending")).toBeVisible();
    // ...but the Pending attempt is not counted as Running (still PROJ-101/0002 +
    // PROJ-103/0001), so the tier stays a genuinely separate, quieter count.
    await expect(page.getByTestId("count-running")).toHaveText("2");
  });

  // drvweb-025 (AC 1): the board self-polls #board-region every 3s. With the morph
  // swap, an unchanged poll makes zero DOM mutations, so a text selection a reader
  // holds in a card survives the tick — the copy-out it used to clobber now works.
  test("a text selection in a board card survives a 3s poll", async ({ page }) => {
    await page.goto("/");
    const card = page.getByTestId("attempt-link-PROJ-102-0001");
    await expect(card).toBeVisible();

    // Select the card's text via a real Range/Selection.
    const selected = await card.evaluate((el) => {
      const r = document.createRange();
      r.selectNodeContents(el);
      const s = window.getSelection()!;
      s.removeAllRanges();
      s.addRange(r);
      return s.toString();
    });
    expect(selected.trim().length).toBeGreaterThan(0);

    // Let at least one 3s board poll land; morph leaves the unchanged card alone.
    await page.waitForTimeout(3600);

    const after = await page.evaluate(() => window.getSelection()?.toString() ?? "");
    expect(after).toBe(selected);
  });
});
