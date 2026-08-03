import { test, expect } from "@playwright/test";

test.describe("board control states", () => {
  test("each seeded ticket lands in its control state", async ({ page }) => {
    await page.goto("/");

    // Counts weighted toward what needs the human.
    await expect(page.getByTestId("count-needs-me")).toHaveText("1");
    await expect(page.getByTestId("count-review")).toHaveText("1");
    await expect(page.getByTestId("count-running")).toHaveText("1");

    // The blocked ticket is on the board (Needs me).
    const needsMe = page.getByTestId("col-needs-me");
    await expect(needsMe.getByTestId("ticket-link-PROJ-101")).toBeVisible();
    await expect(needsMe.getByText("1 open")).toBeVisible();

    // The claimed ticket awaits review.
    await expect(page.getByTestId("col-review").getByTestId("ticket-link-PROJ-102")).toBeVisible();

    // The fresh ticket is quietly running.
    await expect(page.getByTestId("col-running").getByTestId("ticket-link-PROJ-103")).toBeVisible();
  });
});
