import { test, expect } from './helpers';
import { getProjectId } from './helpers';

test.describe.serial('Title Generation Token Stats', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should include title generation tokens in project stats', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Get baseline: total title generation tokens before
    const statsBefore = await page.evaluate(async ({ pid }) => {
      const res = await fetch(`/api/projects/${pid}/stats`);
      return res.json();
    }, { pid: projectId });

    const titleGenTokensBefore = (statsBefore.by_model || [])
      .filter((m: any) => m.provider_name === 'title_generation')
      .reduce((sum: number, m: any) => sum + m.generated_tokens, 0);

    // Count existing cards
    const countBefore = await page.locator('.card').count();

    // Create a new task (triggers title generation)
    await page.click('.add-task-btn');
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });

    const prompt = 'Add input validation to the registration form';
    await page.fill('textarea', prompt);
    await page.click('button[type="submit"]');

    // Wait for modal to close
    await expect(page.locator('.modal')).not.toBeVisible();

    // Wait for a new card to appear
    await page.waitForFunction(
      (expected) => document.querySelectorAll('.card').length >= expected,
      countBefore + 1,
      { timeout: 5000 }
    );

    // Wait for title generation to complete (mock LLM is fast, but give it time)
    await page.waitForTimeout(3000);

    // Get updated stats
    const statsAfter = await page.evaluate(async ({ pid }) => {
      const res = await fetch(`/api/projects/${pid}/stats`);
      return res.json();
    }, { pid: projectId });

    const titleGenTokensAfter = (statsAfter.by_model || [])
      .filter((m: any) => m.provider_name === 'title_generation')
      .reduce((sum: number, m: any) => sum + m.generated_tokens, 0);

    // Title generation tokens should have increased
    expect(titleGenTokensAfter).toBeGreaterThan(titleGenTokensBefore);

    // Verify title_generation entries exist and have valid token counts
    const titleGenEntries = (statsAfter.by_model || []).filter(
      (m: any) => m.provider_name === 'title_generation'
    );
    expect(titleGenEntries.length).toBeGreaterThanOrEqual(1);
    for (const entry of titleGenEntries) {
      expect(entry.prompt_tokens).toBeGreaterThan(0);
      expect(entry.generated_tokens).toBeGreaterThan(0);
    }

    // Total generated tokens should include title tokens
    expect(statsAfter.total_generated_tokens).toBeGreaterThan(
      statsBefore.total_generated_tokens
    );
  });

  test('should show title generation as separate category in stats modal', async ({ page }) => {
    // Open the stats modal
    const statsButton = page.locator('[aria-label="Token usage stats"]');
    await statsButton.click();

    await expect(page.locator('#stats-modal-title')).toBeVisible({ timeout: 5000 });

    // Wait for stats to load
    await page.waitForTimeout(1000);

    // Check that the model table contains a "Title Generation" row
    // The label includes "(Title Generation)" suffix
    const titleGenRow = page.locator('.model-table tbody tr').filter({
      hasText: 'Title Generation',
    });

    // At least one title generation row should exist (from previous test)
    const titleRowCount = await titleGenRow.count();
    expect(titleRowCount).toBeGreaterThanOrEqual(1);
  });
});
