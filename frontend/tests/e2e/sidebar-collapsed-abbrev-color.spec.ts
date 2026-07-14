import { test, expect } from '@playwright/test';

test.describe('Sidebar collapsed abbreviation color', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should show red abbreviation for project with sandbox disabled', async ({ page }) => {
    const collapseBtn = page.locator('.desktop-collapse-btn');
    await expect(collapseBtn).toBeVisible();

    // aria-expanded="true" means sidebar is expanded; "false" means collapsed
    const isExpanded = await collapseBtn.getAttribute('aria-expanded');
    if (isExpanded === 'true') {
      // Sidebar is expanded — click to collapse
      await collapseBtn.click();
    }

    // Wait for sidebar to collapse
    await page.waitForTimeout(300);
    const sidebar = page.locator('.sidebar');
    await expect(sidebar).toHaveClass(/collapsed/);

    // The project abbreviation should be visible
    const abbr = page.locator('.project-abbr');
    await expect(abbr).toBeVisible();

    // The test project has sandbox disabled by default
    // So the abbreviation should have the project-abbr-no-sandbox class
    await expect(abbr).toHaveClass(/project-abbr-no-sandbox/);

    // Verify the computed color is red (rgb values for #f85149 are 248, 81, 73)
    const color = await abbr.evaluate((el) => getComputedStyle(el).color);
    expect(color).toMatch(/248.*81.*73/);
  });
});
