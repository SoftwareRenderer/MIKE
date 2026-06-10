import { test, expect } from './helpers';
import { getProjectId } from './helpers';

test.describe.serial('Token Usage Stats', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should show stats button in top bar', async ({ page }) => {
    const statsButton = page.locator('[aria-label="Token usage stats"]');
    await expect(statsButton).toBeVisible();
  });

  test('should open stats modal on click', async ({ page }) => {
    const statsButton = page.locator('[aria-label="Token usage stats"]');
    await statsButton.click();

    await expect(page.locator('#stats-modal-title')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('#stats-modal-title')).toHaveText('Token Usage');
  });

  test('should load stats from API', async ({ page }) => {
    // Verify the stats API endpoint works
    const projectId = await getProjectId(page);
    const stats = await page.evaluate(async ({ pid }) => {
      const res = await fetch(`/api/projects/${pid}/stats`);
      return res.json();
    }, { pid: projectId });

    expect(stats).toHaveProperty('total_tasks');
    expect(stats).toHaveProperty('total_prompt_tokens');
    expect(stats).toHaveProperty('total_cache_tokens');
    expect(stats).toHaveProperty('total_processed_tokens');
    expect(stats).toHaveProperty('total_generated_tokens');
    expect(stats).toHaveProperty('avg_prompt_tps');
    expect(stats).toHaveProperty('avg_generation_tps');
    expect(stats).toHaveProperty('by_model');
    expect(stats.total_tasks).toBeGreaterThanOrEqual(2); // test project has 2 tasks
  });

  test('should open modal and display stats content', async ({ page }) => {
    const statsButton = page.locator('[aria-label="Token usage stats"]');
    await statsButton.click();

    await expect(page.locator('#stats-modal-title')).toBeVisible({ timeout: 5000 });

    // Wait for loading to finish - modal body should not contain loading text
    const modalBody = page.locator('.modal-body');
    // Either loading disappears or we see content
    await page.waitForTimeout(2000);

    // Check that modal body has some content (not just spinner)
    const childCount = await page.locator('.modal-body > div, .modal-body > table').count();
    expect(childCount).toBeGreaterThanOrEqual(1);
  });

  test('should close modal on close button click', async ({ page }) => {
    const statsButton = page.locator('[aria-label="Token usage stats"]');
    await statsButton.click();
    await expect(page.locator('#stats-modal-title')).toBeVisible({ timeout: 5000 });

    // Click the close button
    await page.locator('[aria-label="Close stats"]').click();

    // Modal should be hidden
    await expect(page.locator('#stats-modal-title')).toBeHidden({ timeout: 5000 });
  });


});
