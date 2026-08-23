import { test, expect, type Browser, type Page } from '@playwright/test';
import { apiPutConfig, apiGetConfig, installPiExtension, deletePiExtension } from './helpers';

/**
 * Agent provider hides operating mode.
 *
 * Agent providers are registry-derived: the only way one exists is with the
 * corresponding agent extension installed AND enabled. This spec installs the
 * local `pi` agent extension on the isolated server (offline — files are
 * already materialized in the repo's extensions/ directory) and uninstalls it
 * afterwards, leaving exactly one LLM provider as the other option.
 *
 * Runs in the `isolated` project: installing an extension mutates server
 * state, and the suite is serialized to avoid races on the shared DB.
 */

const STORAGE_STATE = '/tmp/kanban-test-storage-state.json.isolated';
// browser.newContext() does not reliably inherit the project's use.baseURL —
// pass it explicitly so these API calls hit the isolated server (3001).
const BASE_URL = 'http://localhost:3001';
const LLM_URL = 'http://agent-mode-test:9999/v1';
const LLM_MODEL = 'agent-mode-model';

test.describe.serial('Agent provider hides operating mode', () => {
  let context: Awaited<ReturnType<Browser['newContext']>>;
  let ctxPage: Page;

  test.beforeAll(async ({ browser }) => {
    context = await browser.newContext({ storageState: STORAGE_STATE, baseURL: BASE_URL });
    ctxPage = await context.newPage();
    await ctxPage.goto('/');
    await ctxPage.waitForSelector('.project-item', { timeout: 10000 });

    // Install + enable pi so an agent provider exists in the config.
    await installPiExtension(ctxPage);

    // Exactly one LLM provider (the tests assert a single non-agent option).
    await apiPutConfig(ctxPage, {
      providers: [{ name: 'agent-mode-llm', url: LLM_URL, models: [{ name: LLM_MODEL }], concurrency: 1 }],
    });
    const cfg = await apiGetConfig(ctxPage);
    const id = String(cfg.providers.find((p: any) => p.url === LLM_URL)?.id ?? '');
    expect(id).not.toBe('');
    await apiPutConfig(ctxPage, {
      worker_provider: id, worker_model: LLM_MODEL,
      utility_provider: id, utility_model: LLM_MODEL,
    });
  });

  test.afterAll(async () => {
    // Uninstall pi so the isolated DB stays clean for the other serialized specs.
    await deletePiExtension(ctxPage).catch(() => {});
    await context?.close().catch(() => {});
  });

  test('should hide Operating Mode when agent is selected in Create Task modal', async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });

    // Open the Create Task modal
    await page.locator('.add-task-btn').first().click();
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });

    // Verify Operating Mode is visible with default (LLM) provider
    await expect(page.locator('.config-label:has-text("Operating mode")')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.mode-btn')).toBeVisible();

    // Select the agent provider
    await page.locator('.provider-trigger').click();
    await page.locator('.provider-option-agent').click();
    await page.waitForTimeout(300);

    // Verify Operating Mode is now hidden
    await expect(page.locator('.config-label:has-text("Operating mode")')).not.toBeVisible();
    await expect(page.locator('.mode-btn')).not.toBeVisible();

    // Switch back to LLM provider
    await page.locator('.provider-trigger').click();
    const llmOptions = page.locator('.provider-option:not(.provider-option-agent)');
    await expect(llmOptions).toHaveCount(1);
    await llmOptions.first().click();
    await page.waitForTimeout(300);

    // Verify Operating Mode is visible again
    await expect(page.locator('.config-label:has-text("Operating mode")')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.mode-btn')).toBeVisible();

    // Close modal
    await page.locator('.modal .btn-secondary').click();
    await expect(page.locator('.modal')).not.toBeVisible({ timeout: 5000 });
  });

  test('should hide Operating Mode when agent is selected in Edit Task modal', async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });

    // Get an existing task
    const cards = page.locator('.card');
    await expect(cards.first()).toBeVisible({ timeout: 10000 });
    const taskId = await cards.first().getAttribute('data-task-id') || '';
    expect(taskId).not.toBe('');

    // Navigate to the task to see the card with edit button
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Wait for the selected card to be visible
    await expect(page.locator('.card.selected')).toBeVisible({ timeout: 5000 });

    // Open the edit modal
    const editButton = page.locator('.card.selected').locator('button[aria-label="Edit task"]');
    await expect(editButton).toBeVisible();
    await editButton.click();

    const modal = page.locator('.modal[aria-labelledby="modal-title"]');
    await expect(modal).toBeVisible({ timeout: 5000 });

    // Verify Operating Mode is visible (task was created with LLM provider)
    await expect(modal.locator('.config-label:has-text("Operating mode")')).toBeVisible();
    await expect(modal.locator('.mode-btn')).toBeVisible();

    // Select the agent provider (scope to modal)
    await modal.locator('.provider-trigger').click();
    await page.locator('.provider-option-agent').click();
    await page.waitForTimeout(300);

    // Verify Operating Mode is now hidden
    await expect(modal.locator('.config-label:has-text("Operating mode")')).not.toBeVisible();
    await expect(modal.locator('.mode-btn')).not.toBeVisible();

    // Switch back to LLM provider
    await modal.locator('.provider-trigger').click();
    const llmOptions = page.locator('.provider-option:not(.provider-option-agent)');
    await expect(llmOptions).toHaveCount(1);
    await llmOptions.first().click();
    await page.waitForTimeout(300);

    // Verify Operating Mode is visible again
    await expect(modal.locator('.config-label:has-text("Operating mode")')).toBeVisible({ timeout: 5000 });
    await expect(modal.locator('.mode-btn')).toBeVisible();

    // Cancel without saving
    await page.locator('.modal .btn-secondary').click();
    await expect(modal).not.toBeVisible({ timeout: 5000 });
  });
});
