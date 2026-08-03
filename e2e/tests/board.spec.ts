import { test, expect } from "@playwright/test";

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
});
