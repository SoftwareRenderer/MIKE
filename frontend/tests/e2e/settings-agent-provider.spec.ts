import { test, expect, type Browser, type Page } from '@playwright/test';
import { apiPutConfig, apiGetConfig, waitForConfig, installPiExtension, deletePiExtension, getPiExtensionId, csrfFetch } from './helpers';

/**
 * Agent provider management — Settings → Providers.
 *
 * Agent providers (e.g. Pi Coding Agent) are registry-derived: they exist only
 * while the corresponding agent extension is installed AND enabled. This spec
 * installs the local `pi` extension on the isolated server and verifies:
 *   1. the agent card renders WITHOUT a remove button (and no "+ Add Agent"
 *      button exists — providers arrive via the extension lifecycle);
 *   2. disabling the extension removes the provider, and a Worker reference
 *      left dangling by that forces re-selection (save is blocked);
 *   3. re-enabling the extension restores the provider automatically.
 *
 * Runs in the `isolated` project: installing/uninstalling an extension mutates
 * server state, and the suite is serialized to avoid races on the shared DB.
 */

const STORAGE_STATE = '/tmp/kanban-test-storage-state.json.isolated';
// browser.newContext() does not reliably inherit the project's use.baseURL —
// pass it explicitly so these API calls hit the isolated server (3001).
const BASE_URL = 'http://localhost:3001';
const LLM_URL = 'http://agent-prov-test:9999/v1';
const LLM_MODEL = 'agent-prov-model';

test.describe.serial('Agent provider management (Settings → Providers)', () => {
  let context: Awaited<ReturnType<Browser['newContext']>>;
  let page: Page;

  /** Enable/disable the installed pi extension on the isolated server. */
  async function setPiEnabled(enabled: boolean) {
    const extId = await getPiExtensionId(page);
    expect(extId).not.toBe('');
    await csrfFetch(page, `/api/extensions/${extId}/enabled`, { method: 'PATCH', body: { enabled } });
  }

  test.beforeAll(async ({ browser }) => {
    context = await browser.newContext({ storageState: STORAGE_STATE, baseURL: BASE_URL });
    page = await context.newPage();
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });

    // Install + enable the local pi agent extension (offline — files are
    // already materialized in the repo's extensions/ directory).
    await installPiExtension(page);

    // Baseline: exactly one LLM provider as worker/utility.
    await apiPutConfig(page, {
      providers: [{ name: 'agent-prov-llm', url: LLM_URL, models: [{ name: LLM_MODEL }], concurrency: 1 }],
    });
    const cfg = await apiGetConfig(page);
    const id = String(cfg.providers.find((p: any) => p.url === LLM_URL)?.id ?? '');
    expect(id).not.toBe('');
    await apiPutConfig(page, {
      worker_provider: id, worker_model: LLM_MODEL,
      utility_provider: id, utility_model: LLM_MODEL,
    });
    // The installed+enabled extension must surface as an agent provider.
    await waitForConfig(page, c =>
      c.providers.some((p: any) => p.url === 'agent://pi') && c.worker_provider === id, 10000);
  });

  test.afterAll(async () => {
    // Uninstall pi so the isolated DB stays clean for the other serialized specs.
    await deletePiExtension(page).catch(() => {});
    await context?.close().catch(() => {});
  });

  test('agent provider card has no remove button and no Add Agent button', async () => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    await page.click('.icon-btn[aria-label="Settings"]');
    await page.waitForSelector('[role="dialog"]', { timeout: 5000 });

    // The Pi card renders from the registry (installed + enabled extension).
    const agentCard = page.locator('.provider-card', { hasText: 'Pi Coding Agent' });
    await expect(agentCard).toBeVisible({ timeout: 5000 });

    // No remove button on the agent card...
    expect(await agentCard.locator('.btn-remove-provider').count()).toBe(0);
    // ...but the LLM card keeps its remove button.
    const llmCard = page.locator('.provider-card', { hasText: 'agent-prov-test:9999' });
    await expect(llmCard).toBeVisible({ timeout: 5000 });
    expect(await llmCard.locator('.btn-remove-provider').count()).toBe(1);

    // Expand the agent card — the management note lives in the details.
    await agentCard.locator('.provider-card-header').click();
    await expect(agentCard.locator('.provider-details')).toBeVisible({ timeout: 5000 });
    await expect(agentCard.locator('.agent-managed-note')).toContainText('Settings → Extensions');

    // No "Add Agent" button anywhere in the modal.
    expect(await page.locator('button[title="Add a coding agent"]').count()).toBe(0);
    expect(await page.locator('.agent-menu').count()).toBe(0);

    // Close without saving (nothing was changed).
    await page.click('button:has-text("Cancel")');
    await expect(page.locator('.modal')).not.toBeVisible({ timeout: 5000 });
  });

  test('disabling the extension removes the provider and forces re-selection', async () => {
    // Point the Worker pair at the agent (by name — the agent reference form).
    await apiPutConfig(page, { worker_provider: 'pi', worker_model: 'pi' });
    await waitForConfig(page, c => c.worker_provider === 'pi');

    // Disable the pi extension.
    await setPiEnabled(false);
    // The agent provider disappears from the config (registry reloaded + pruned).
    await waitForConfig(page, c => !c.providers.some((p: any) => p.url === 'agent://pi'), 10000);

    // Fresh load so the app picks up the new config, then open Settings.
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    await page.click('.icon-btn[aria-label="Settings"]');
    await page.waitForSelector('[role="dialog"]', { timeout: 5000 });

    // No Pi card anymore.
    expect(await page.locator('.provider-card', { hasText: 'Pi Coding Agent' }).count()).toBe(0);

    // The dangling Worker reference renders as the empty placeholder...
    const workerSelect = page.locator('.primary-provider-row').nth(0).locator('.primary-provider-select').nth(0);
    await expect(workerSelect).toHaveValue('');

    // ...and Save is blocked until it is re-pointed.
    await page.getByRole('button', { name: 'Save' }).click();
    await expect(page.locator('#config-error')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('#config-error')).toContainText('Worker');

    // Re-point Worker at the LLM provider + model → Save succeeds.
    const cfg = await apiGetConfig(page);
    const llmId = String(cfg.providers.find((p: any) => p.url === LLM_URL)?.id ?? '');
    expect(llmId).not.toBe('');
    await workerSelect.selectOption(llmId);
    await page.locator('.primary-provider-row').nth(0).locator('.primary-provider-select').nth(1).selectOption(LLM_MODEL);
    await page.getByRole('button', { name: 'Save' }).click();
    await expect(page.locator('.modal')).not.toBeVisible({ timeout: 10000 });

    const saved = await apiGetConfig(page);
    expect(saved.worker_provider).toBe(llmId);
    expect(saved.providers.some((p: any) => p.url === 'agent://pi')).toBe(false);
  });

  test('re-enabling the extension restores the provider automatically', async () => {
    await setPiEnabled(true);
    await waitForConfig(page, c => c.providers.some((p: any) => p.url === 'agent://pi'), 10000);

    // The card is back in Settings → Providers.
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    await page.click('.icon-btn[aria-label="Settings"]');
    await page.waitForSelector('[role="dialog"]', { timeout: 5000 });
    await expect(page.locator('.provider-card', { hasText: 'Pi Coding Agent' })).toBeVisible({ timeout: 5000 });

    await page.click('button:has-text("Cancel")');
    await expect(page.locator('.modal')).not.toBeVisible({ timeout: 5000 });
  });
});
