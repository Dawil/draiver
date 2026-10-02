import { test, expect } from "@playwright/test";
import path from "node:path";

// drv-021: the attempt page's top-of-page panels are folded into one bordered
// tabbed card (Spec default), the Agent Logs is a bordered collapsible peer card,
// and the Log section stays last. These tests pin the client-side tab switching,
// its keyboard a11y, the conditional-tab gating, and the Agent Logs affordance —
// and capture the new-UI screenshots the ticket asks for as evidence.

// PROJ-102/0001 is the fully-provisioned fixture (Review with repo+base → Git tab;
// a seeded BDD run → BDD report tab), so it shows every tab at once.
const RICH = "/ticket/PROJ-102/0001";
const shotsDir = path.join(__dirname, "..", "screenshots");

const TABS = ["spec", "provenance", "git", "bdd", "cache"] as const;
type Tab = (typeof TABS)[number];

test.describe("attempt page — tabbed top section", () => {
  test("renders a tabbed card with Spec selected by default", async ({ page }) => {
    await page.goto(RICH);
    await expect(page.getByTestId("attempt-tabs")).toBeVisible();
    await expect(page.getByTestId("attempt-tablist")).toHaveAttribute("role", "tablist");

    // Spec is the selected tab on load; its panel is shown, the rest hidden.
    await expect(page.getByTestId("attempt-tab-spec")).toHaveAttribute("aria-selected", "true");
    await expect(page.getByTestId("attempt-tabpanel-spec")).toBeVisible();
    for (const tab of ["provenance", "git", "bdd", "cache"] as Tab[]) {
      await expect(page.getByTestId(`attempt-tab-${tab}`)).toHaveAttribute("aria-selected", "false");
      await expect(page.getByTestId(`attempt-tabpanel-${tab}`)).toBeHidden();
    }
    // The spec's own content (and its "more" cap button) resolve inside the panel.
    await expect(page.getByTestId("attempt-tabpanel-spec").getByTestId("spec")).toBeVisible();
  });

  test("selecting each tab reveals its panel and hides the rest, with no reload", async ({ page }) => {
    await page.goto(RICH);
    // A sentinel on window proves the page never navigated/reloaded while switching.
    await page.evaluate(() => ((window as any).__noReload = true));

    for (const tab of TABS) {
      await page.getByTestId(`attempt-tab-${tab}`).click();
      await expect(page.getByTestId(`attempt-tab-${tab}`)).toHaveAttribute("aria-selected", "true");
      await expect(page.getByTestId(`attempt-tabpanel-${tab}`)).toBeVisible();
      // Exactly one panel is visible at a time.
      for (const other of TABS) {
        if (other !== tab) await expect(page.getByTestId(`attempt-tabpanel-${other}`)).toBeHidden();
      }
    }
    expect(await page.evaluate(() => (window as any).__noReload)).toBe(true);
  });

  test("tabs are keyboard-operable: focus the tablist, arrow keys move selection", async ({ page }) => {
    await page.goto(RICH);
    const spec = page.getByTestId("attempt-tab-spec");
    await spec.focus();
    await expect(spec).toBeFocused();

    // ArrowRight advances selection and focus to the next tab (roving tabindex),
    // and aria-selected tracks the active tab.
    await page.keyboard.press("ArrowRight");
    const prov = page.getByTestId("attempt-tab-provenance");
    await expect(prov).toBeFocused();
    await expect(prov).toHaveAttribute("aria-selected", "true");
    await expect(prov).toHaveAttribute("tabindex", "0");
    await expect(spec).toHaveAttribute("aria-selected", "false");
    await expect(spec).toHaveAttribute("tabindex", "-1");
    await expect(page.getByTestId("attempt-tabpanel-provenance")).toBeVisible();

    // ArrowLeft wraps back to Spec.
    await page.keyboard.press("ArrowLeft");
    await expect(spec).toBeFocused();
    await expect(spec).toHaveAttribute("aria-selected", "true");

    // End jumps to the last tab, Home back to the first.
    await page.keyboard.press("End");
    await expect(page.getByTestId("attempt-tab-cache")).toBeFocused();
    await page.keyboard.press("Home");
    await expect(spec).toBeFocused();
  });

  test("conditional tabs follow their panel's gate (BDD absent without runs, present with one)", async ({ page }) => {
    // PROJ-101/0001 has no captured BDD run → no BDD tab, but the always-present
    // Spec / Provenance / Cache tabs are there. (The Git-tab gate on base/repo & not
    // Done is pinned at the unit level in internal/web: TestGitControlsGating and
    // TestAttemptTabsStructure.)
    await page.goto("/ticket/PROJ-101/0001");
    await expect(page.getByTestId("attempt-tabs")).toBeVisible();
    await expect(page.getByTestId("attempt-tab-spec")).toBeVisible();
    await expect(page.getByTestId("attempt-tab-provenance")).toBeVisible();
    await expect(page.getByTestId("attempt-tab-cache")).toBeVisible();
    await expect(page.getByTestId("attempt-tab-bdd")).toHaveCount(0);

    // PROJ-102/0001 has a captured run and a base+repo → both conditional tabs show.
    await page.goto(RICH);
    await expect(page.getByTestId("attempt-tab-bdd")).toBeVisible();
    await expect(page.getByTestId("attempt-tab-git")).toBeVisible();
  });

  test("Agent Logs is a bordered card below the tabs, collapsed by default, header toggles it", async ({ page }) => {
    await page.goto(RICH);
    const details = page.getByTestId("agent-logs-details");
    const summary = page.getByTestId("agent-logs-summary");

    // Agent Logs sits below the tabbed card and above the Log section.
    const yTabs = (await page.getByTestId("attempt-tabs").boundingBox())!.y;
    const yAgent = (await page.getByTestId("agent-logs").boundingBox())!.y;
    const yLog = (await page.getByTestId("log-region").boundingBox())!.y;
    expect(yTabs).toBeLessThan(yAgent);
    expect(yAgent).toBeLessThan(yLog);

    // Collapsed by default: the stream is not visible.
    await expect(details).not.toHaveAttribute("open", /.*/);
    await expect(page.getByTestId("agent-logs-stream")).toBeHidden();

    // The whole header row is the hitbox: the summary fills (nearly) the card width.
    const cardW = (await page.getByTestId("agent-logs").boundingBox())!.width;
    const sumBox = (await summary.boundingBox())!;
    expect(sumBox.width).toBeGreaterThan(cardW * 0.9);

    // Activating the header expands it and the stream region appears.
    await summary.click();
    await expect(details).toHaveAttribute("open", /.*/);
    await expect(page.getByTestId("agent-logs-stream")).toBeVisible();

    // Clicking again collapses it.
    await summary.click();
    await expect(details).not.toHaveAttribute("open", /.*/);
  });

  test("the Log heading and entries remain the last section and still poll", async ({ page }) => {
    await page.goto(RICH);
    await expect(page.getByTestId("log-region")).toHaveAttribute("hx-get", "/ticket/PROJ-102/0001/live");
    await expect(page.getByTestId("log-region")).toHaveAttribute("hx-trigger", "every 3s");
    await expect(page.getByTestId("log-compose")).toBeVisible();
    await expect(page.getByTestId("log-timeline")).toBeVisible();
  });

  // New-UI evidence: screenshots of the reorganised page, each tab, and Agent Logs
  // collapsed vs expanded. Saved under e2e/screenshots/ for attachment to the review.
  test("capture screenshots of the reorganised attempt page", async ({ page }) => {
    await page.goto(RICH);
    await page.screenshot({ path: path.join(shotsDir, "01-page-default-spec-agentlogs-collapsed.png"), fullPage: true });

    for (const tab of TABS) {
      await page.getByTestId(`attempt-tab-${tab}`).click();
      await expect(page.getByTestId(`attempt-tabpanel-${tab}`)).toBeVisible();
      await page.screenshot({ path: path.join(shotsDir, `02-tab-${tab}.png`), fullPage: true });
    }

    // Back to Spec, then Agent Logs collapsed and expanded.
    await page.getByTestId("attempt-tab-spec").click();
    await page.getByTestId("agent-logs").scrollIntoViewIfNeeded();
    await page.screenshot({ path: path.join(shotsDir, "03-agent-logs-collapsed.png") });
    await page.getByTestId("agent-logs-summary").click();
    await expect(page.getByTestId("agent-logs-details")).toHaveAttribute("open", /.*/);
    await page.screenshot({ path: path.join(shotsDir, "04-agent-logs-expanded.png") });
  });
});
