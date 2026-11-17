import { test, expect, type Page } from '@playwright/test';
import { getProjectId } from './helpers';

/** Dismiss the Create Task modal. Named by its label: the shared button family carries no class a
 *  restyle has to keep, so a styling class here is a locator that breaks on a rename. */
const dismissTaskModal = (page: Page) =>
  page.locator('.modal').getByRole('button', { name: 'Cancel', exact: true });

// The mode the picker shows is the mode the task is created with. The picker's own choices, the
// auto-switch rule behind the "?" behaviour (`applyExploreModeRule`) and the "user chose it" override
// are `utils/__tests__/explore-mode.unit.ts`; what needs the browser is that the modal wires that rule
// to its button and that the choice reaches the board row — `operating_mode` is what later decides which
// tools the turn is given.

test.describe.serial('Create Task mode select', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('offers Act, Plan and Explore, and the mode chosen from the dropdown is the one shown', async ({ page }) => {
    // Open the Create Task modal
    await page.locator('.add-task-btn').first().click();
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });

    // Click the mode selector button
    await page.locator('.mode-btn').click();

    // Verify dropdown is open with all three options
    const dropdown = page.locator('.mode-dropdown');
    await expect(dropdown).toBeVisible({ timeout: 5000 });

    // Verify all three mode options are present
    const options = page.locator('.mode-option');
    await expect(options).toHaveCount(3);

    // Verify the labels
    await expect(options.nth(0)).toContainText('Act');
    await expect(options.nth(1)).toContainText('Plan');
    await expect(options.nth(2)).toContainText('Explore');

    // Select Plan mode
    await page.locator('.mode-option.plan').click();

    // Verify the button now shows "Plan", with its icon, and the dropdown closed after the choice
    await expect(page.locator('.mode-btn .mode-label')).toHaveText('Plan');
    await expect(page.locator('.mode-btn .mode-icon.plan')).toBeVisible();
    await expect(page.locator('.mode-dropdown')).not.toBeVisible({ timeout: 5000 });

    // Close modal
    await dismissTaskModal(page).click();
    await expect(page.locator('.modal')).not.toBeVisible({ timeout: 5000 });
  });

  test('a ? in the prompt switches the picker to Explore, and the created task carries that mode', async ({ page }) => {
    const projectId = await getProjectId(page);
    const uniquePrompt = `explore-qs-${Date.now()}`;

    // Open the Create Task modal
    await page.locator('.add-task-btn').first().click();
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });

    // Default mode is Act in the test environment
    await expect(page.locator('.mode-btn .mode-label')).toHaveText('Act');

    // Type a description containing a question mark
    await page.locator('#task-description').fill(`${uniquePrompt}? What is going on?`);

    // The mode should auto-switch to Explore, icon and all
    await expect(page.locator('.mode-btn .mode-label')).toHaveText('Explore');
    await expect(page.locator('.mode-btn .mode-icon.explore')).toBeVisible();

    await page.locator('.modal .btn-primary').click();
    await expect(page.locator('.modal')).not.toBeVisible({ timeout: 5000 });

    // Verify the created task persisted with operating_mode=explore
    const task = await page.evaluate(async ({ pid, prompt }) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      const board = await res.json();
      return board.tasks.find((t: any) => t.prompt?.includes(prompt)) || null;
    }, { pid: projectId, prompt: uniquePrompt });

    expect(task).not.toBeNull();
    expect(task.operating_mode).toBe('explore');
  });
});
