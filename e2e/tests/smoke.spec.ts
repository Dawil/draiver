import { test, expect } from "@playwright/test";

test.describe("board shell", () => {
  test("loads and renders the four control states", async ({ page }) => {
    await page.goto("/");
    await expect(page.getByRole("heading", { name: "Draiver", level: 1 })).toBeVisible();
    await expect(page.getByTestId("board")).toBeVisible();
    for (const col of ["col-running", "col-stuck", "col-review", "col-done"]) {
      await expect(page.getByTestId(col)).toBeVisible();
    }
  });

  test("ships the self-contained polling scripts (no CDN)", async ({ page }) => {
    // htmx and the idiomorph morph extension (drvweb-025) are both self-hosted
    // from /static — the board's CSP/offline posture rules out a CDN.
    for (const asset of ["/static/htmx.min.js", "/static/idiomorph-ext.min.js"]) {
      const res = await page.request.get(asset);
      expect(res.ok(), `${asset} should be served locally`).toBeTruthy();
      expect(res.headers()["content-type"]).toContain("javascript");
    }

    // No <script> on the board page points at an external origin.
    await page.goto("/");
    const externalScripts = await page
      .locator("script[src]")
      .evaluateAll((els) =>
        els
          .map((e) => (e as HTMLScriptElement).getAttribute("src") || "")
          .filter((src) => /^(https?:)?\/\//.test(src)),
      );
    expect(externalScripts).toEqual([]);
  });
});
