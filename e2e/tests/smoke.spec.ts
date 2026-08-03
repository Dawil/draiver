import { test, expect } from "@playwright/test";

test.describe("board shell", () => {
  test("loads and renders the four control states", async ({ page }) => {
    await page.goto("/");
    await expect(page.getByRole("heading", { name: "Draiver", level: 1 })).toBeVisible();
    await expect(page.getByTestId("board")).toBeVisible();
    for (const col of ["col-needs-me", "col-review", "col-running", "col-done"]) {
      await expect(page.getByTestId(col)).toBeVisible();
    }
  });

  test("ships the self-contained polling script (no CDN)", async ({ page }) => {
    const res = await page.request.get("/static/htmx.min.js");
    expect(res.ok()).toBeTruthy();
    expect(res.headers()["content-type"]).toContain("javascript");
  });
});
