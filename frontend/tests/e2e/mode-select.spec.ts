import { test, expect, type Page } from '@playwright/test';
import { getProjectId } from './helpers';

/** Dismiss the Create Task modal. Named by its label: the shared button family carries no class a
 *  restyle has to keep, so a styling class here is a locator that breaks on a rename. */
const dismissTaskModal = (page: Page) =>
  page.locator('.modal').getByRole('button', { name: 'Cancel', exact: true });

test.describe.serial('ModeSelect component', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('should open mode dropdown showing Act/Plan/Explore options', async ({ page }) => {
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

    // Close modal
    await dismissTaskModal(page).click();
    await expect(page.locator('.modal')).not.toBeVisible({ timeout: 5000 });
  });

  test('should switch to Plan mode', async ({ page }) => {
    // Open the Create Task modal
    await page.locator('.add-task-btn').first().click();
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });

    // Click the mode selector to open dropdown
    await page.locator('.mode-btn').click();
    await expect(page.locator('.mode-dropdown')).toBeVisible({ timeout: 5000 });

    // Select Plan mode
    await page.locator('.mode-option.plan').click();

    // Verify the button now shows "Plan"
    await expect(page.locator('.mode-btn .mode-label')).toHaveText('Plan');

    // Verify the plan icon is active
    await expect(page.locator('.mode-btn .mode-icon.plan')).toBeVisible();

    // Verify dropdown closed after selection
    await expect(page.locator('.mode-dropdown')).not.toBeVisible({ timeout: 5000 });

    // Close modal
    await dismissTaskModal(page).click();
    await expect(page.locator('.modal')).not.toBeVisible({ timeout: 5000 });
  });

  test('should switch back to Act mode', async ({ page }) => {
    // Open the Create Task modal
    await page.locator('.add-task-btn').first().click();
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });

    // First switch to Plan to demonstrate switching back
    await page.locator('.mode-btn').click();
    await page.locator('.mode-option.plan').click();
    await expect(page.locator('.mode-btn .mode-label')).toHaveText('Plan');

    // Now switch back to Act
    await page.locator('.mode-btn').click();
    await expect(page.locator('.mode-dropdown')).toBeVisible({ timeout: 5000 });
    await page.locator('.mode-option.act').click();

    // Verify the button now shows "Act"
    await expect(page.locator('.mode-btn .mode-label')).toHaveText('Act');

    // Verify the act icon is active
    await expect(page.locator('.mode-btn .mode-icon.act')).toBeVisible();

    // Verify dropdown closed after selection
    await expect(page.locator('.mode-dropdown')).not.toBeVisible({ timeout: 5000 });

    // Close modal
    await dismissTaskModal(page).click();
    await expect(page.locator('.modal')).not.toBeVisible({ timeout: 5000 });
  });

  test('should close dropdown when clicking outside', async ({ page }) => {
    // Open the Create Task modal
    await page.locator('.add-task-btn').first().click();
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });

    // Click the mode selector to open dropdown
    await page.locator('.mode-btn').click();
    await expect(page.locator('.mode-dropdown')).toBeVisible({ timeout: 5000 });

    // Click outside the dropdown (on the modal background/body area)
    // Click on the textarea which is outside the mode-select element
    await page.locator('.modal textarea').click();

    // Verify the dropdown closed
    await expect(page.locator('.mode-dropdown')).not.toBeVisible({ timeout: 5000 });

    // Verify the button is still visible (modal not closed)
    await expect(page.locator('.mode-btn')).toBeVisible();

    // Close modal
    await dismissTaskModal(page).click();
    await expect(page.locator('.modal')).not.toBeVisible({ timeout: 5000 });
  });
});

test.describe.serial('Question mark auto-switches operating mode to Explore', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('should switch to Explore when the description contains a ?', async ({ page }) => {
    // Open the Create Task modal
    await page.locator('.add-task-btn').first().click();
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });

    // Default mode is Act in the test environment
    await expect(page.locator('.mode-btn .mode-label')).toHaveText('Act');

    // Type a description containing a question mark
    await page.locator('#task-description').fill('Why is the login broken?');

    // The mode should auto-switch to Explore
    await expect(page.locator('.mode-btn .mode-label')).toHaveText('Explore');

    // The explore icon should be active
    await expect(page.locator('.mode-btn .mode-icon.explore')).toBeVisible();

    // Close modal (prompt is non-empty → confirm discard)
    await dismissTaskModal(page).click();
    await expect(page.getByRole('button', { name: 'Discard' })).toBeVisible({ timeout: 5000 });
    await page.getByRole('button', { name: 'Discard' }).click();
    await expect(page.locator('.modal')).not.toBeVisible({ timeout: 5000 });
  });

  test('should revert to the previous mode when the ? is removed', async ({ page }) => {
    // Open the Create Task modal
    await page.locator('.add-task-btn').first().click();
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });

    await expect(page.locator('.mode-btn .mode-label')).toHaveText('Act');

    // Add a ? — mode switches to Explore
    await page.locator('#task-description').fill('Why is the login broken?');
    await expect(page.locator('.mode-btn .mode-label')).toHaveText('Explore');

    // Remove the ? — mode should revert to Act
    await page.locator('#task-description').fill('Why is the login broken');
    await expect(page.locator('.mode-btn .mode-label')).toHaveText('Act');

    // Type ? again and remove it again — still reverts
    await page.locator('#task-description').fill('Why is the login broken?');
    await expect(page.locator('.mode-btn .mode-label')).toHaveText('Explore');
    await page.locator('#task-description').fill('no question mark here');
    await expect(page.locator('.mode-btn .mode-label')).toHaveText('Act');

    // Close modal (prompt is non-empty → confirm discard)
    await dismissTaskModal(page).click();
    await expect(page.getByRole('button', { name: 'Discard' })).toBeVisible({ timeout: 5000 });
    await page.getByRole('button', { name: 'Discard' }).click();
    await expect(page.locator('.modal')).not.toBeVisible({ timeout: 5000 });
  });

  test('should not override a manually selected mode', async ({ page }) => {
    // Open the Create Task modal
    await page.locator('.add-task-btn').first().click();
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });

    // Manually select Plan mode
    await page.locator('.mode-btn').click();
    await expect(page.locator('.mode-dropdown')).toBeVisible({ timeout: 5000 });
    await page.locator('.mode-option.plan').click();
    await expect(page.locator('.mode-btn .mode-label')).toHaveText('Plan');

    // Add a ? — mode should STAY Plan (user changed it)
    await page.locator('#task-description').fill('Why is the login broken?');
    await expect(page.locator('.mode-btn .mode-label')).toHaveText('Plan');

    // Remove the ? — mode should STAY Plan (no revert)
    await page.locator('#task-description').fill('Why is the login broken');
    await expect(page.locator('.mode-btn .mode-label')).toHaveText('Plan');

    // Close modal (prompt is non-empty → confirm discard)
    await dismissTaskModal(page).click();
    await expect(page.getByRole('button', { name: 'Discard' })).toBeVisible({ timeout: 5000 });
    await page.getByRole('button', { name: 'Discard' }).click();
    await expect(page.locator('.modal')).not.toBeVisible({ timeout: 5000 });
  });

  test('should create the task in Explore mode when the description contains a ?', async ({ page }) => {
    const projectId = await getProjectId(page);
    const uniquePrompt = `explore-qs-${Date.now()}`;

    // Open the Create Task modal
    await page.locator('.add-task-btn').first().click();
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });

    await page.locator('#task-description').fill(`${uniquePrompt}? What is going on?`);
    await expect(page.locator('.mode-btn .mode-label')).toHaveText('Explore');

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
