import { test, expect } from '@playwright/test';
import { apiPutConfig, waitForConfig, getProjectId } from './helpers';

test.describe.serial('Reasoning Effort in Model Dropdowns', () => {
  test.beforeEach(async ({ page }) => {
    await page.keyboard.press('Escape').catch(() => {});
    await expect(page.locator('.modal')).not.toBeVisible({ timeout: 2000 }).catch(() => {});

    // Set up a provider with duplicate models at different reasoning efforts
    await apiPutConfig(page, {
      providers: [
        {
          name: 'effort-test',
          display_name: 'Effort Test',
          url: 'http://effort-test:9999/v1',
          models: [
            { name: 'model-a', reasoning_effort: 'medium' },
            { name: 'model-a', reasoning_effort: 'high' },
            { name: 'model-b', reasoning_effort: 'off' },
            { name: 'model-c', reasoning_effort: 'medium' },
          ],
          concurrency: 1,
          default_model: 'model-a',
        },
      ],
      default_provider: '',
    });
    await waitForConfig(page, c =>
      c.providers && c.providers.length === 1 && c.providers[0].name === 'effort-test'
    );
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('should show unique model+effort pairs in create task modal with badges', async ({ page }) => {
    // Reload to ensure frontend picks up the config
    await page.reload();
    await page.waitForSelector('.card', { timeout: 10000 });

    // Open create task modal
    await page.click('.add-task-btn');
    await page.waitForSelector('.modal', { timeout: 5000 });

    // Wait for model selector to appear (providers may load async)
    await expect(page.locator('.model-trigger')).toBeVisible({ timeout: 10000 });

    // Open model selector dropdown
    await page.locator('.model-trigger').click();
    await page.waitForSelector('.portal-model-dropdown', { timeout: 5000 });

    // Should have 4 options: model-a (medium), model-a (high), model-b (off), model-c (medium)
    const modelOptions = page.locator('.model-option');
    await expect(modelOptions).toHaveCount(4);

    // Check badges: only "high" and "off" should have badges (medium is default, hidden)
    const badges = page.locator('.effort-badge');
    const badgeCount = await badges.count();
    expect(badgeCount).toBe(2);

    const badgeTexts: string[] = [];
    for (let i = 0; i < badgeCount; i++) {
      badgeTexts.push(await badges.nth(i).textContent());
    }
    expect(badgeTexts).toContain('high');
    expect(badgeTexts).toContain('off');
  });

  test('should store reasoning_effort when creating a task with model+effort selection', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Open create task modal
    await page.click('.add-task-btn');
    await page.waitForSelector('.modal', { timeout: 5000 });

    // Fill prompt
    await page.fill('textarea', `reasoning-effort-test-${Date.now()}`);

    // Open model selector and pick model-a with high effort
    await page.locator('.model-trigger').click();
    await page.waitForSelector('.portal-model-dropdown', { timeout: 5000 });

    // Find the option with "model-a" and "high" badge
    const modelOptions = page.locator('.model-option');
    const count = await modelOptions.count();
    let clicked = false;
    for (let i = 0; i < count; i++) {
      const option = modelOptions.nth(i);
      const nameEl = option.locator('.option-name');
      const badgeEl = option.locator('.effort-badge');
      const name = await nameEl.textContent();
      const hasBadge = await badgeEl.isVisible();
      if (name === 'model-a' && hasBadge) {
        await option.click();
        clicked = true;
        break;
      }
    }
    expect(clicked).toBe(true);

    // Wait for dropdown to close
    await expect(page.locator('.portal-model-dropdown')).not.toBeVisible({ timeout: 3000 });

    // Submit
    await page.getByRole('button', { name: 'Create Task' }).click();
    await expect(page.locator('.modal')).not.toBeVisible({ timeout: 10000 });

    // Verify the task was created with the correct reasoning_effort
    const tasks = await page.evaluate(async ({ pid }) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      const board = await res.json();
      return board.tasks.filter((t: any) => t.prompt.includes('reasoning-effort-test'));
    }, { pid: projectId });

    expect(tasks.length).toBeGreaterThan(0);
    const task = tasks[tasks.length - 1];
    expect(task.model_name).toBe('model-a');
    expect(task.reasoning_effort).toBe('high');
  });

  test('should show effort badges in card model picker dropdown', async ({ page }) => {
    // Click the model badge on the first card to open the picker
    const firstCard = page.locator('.card').first();
    const modelBadge = firstCard.locator('.badge.model-badge');
    if (!(await modelBadge.isVisible().catch(() => false))) {
      test.skip();
    }
    await modelBadge.click();
    await page.waitForSelector('.portal-card-model-picker', { timeout: 5000 });

    // Should have model options with badges
    const badges = page.locator('.portal-card-model-picker .effort-badge');
    const badgeCount = await badges.count();
    expect(badgeCount).toBe(2);

    const badgeTexts: string[] = [];
    for (let i = 0; i < badgeCount; i++) {
      badgeTexts.push(await badges.nth(i).textContent());
    }
    expect(badgeTexts).toContain('high');
    expect(badgeTexts).toContain('off');

    // Close picker
    await page.keyboard.press('Escape');
  });
});
