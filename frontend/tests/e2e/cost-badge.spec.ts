import { test, expect } from '@playwright/test';
import {
  getProjectId,
  createTaskViaApi,
  navigateToTask,
  waitForChatForm,
  apiPutConfig,
  waitForConfig,
} from './helpers';

// ─── Config: provider with per-token pricing ────────────────────────────────

const BASE_CONFIG = {
  workspace_dir: './.worktrees',
  default_auto_commit: false,
  default_plan_mode: false,
  default_max_failed_turns: 3,
};

function pricingConfig() {
  return {
    ...BASE_CONFIG,
    providers: [
      {
        name: 'cost-provider',
        display_name: 'Cost Provider',
        url: 'http://localhost:9999/v1',
        models: [
          {
            name: 'model-a',
            reasoning_effort: 'off',
            context_window: 32768,
            input_price: 3.0,
            output_price: 5.0,
            cache_read_price: 1.0,
            cache_write_price: 0,
          },
        ],
        concurrency: 1,
        input_price: 3.0,
        output_price: 5.0,
        cache_read_price: 1.0,
      },
    ],
    worker_provider: 'cost-provider',
    worker_model: 'model-a',
    utility_provider: 'cost-provider',
    utility_model: 'model-a',
  };
}

// ─── Cost badge near the Context meter ──────────────────────────────────────

test.describe.serial('LLM Session Cost Badge', () => {
  test.beforeEach(async ({ page }) => {
    await apiPutConfig(page, pricingConfig());
    await waitForConfig(page, c =>
      c.providers && c.providers.length === 1 && c.providers[0].url === 'http://localhost:9999/v1' &&
      c.providers[0].input_price === 3.0
    , 5000);
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('shows session cost near the Context meter when the model has per-token pricing', async ({ page }) => {
    test.setTimeout(45000);
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `cost-badge-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await waitForChatForm(page);

    // One mock call: 45 prompt + 8 completion tokens at $3/$5 per 1M → $0.000175 → $0.0002.
    await expect(page.locator('.chat-message.assistant').first()).toBeVisible({ timeout: 15000 });

    // The Context meter shows the session cost badge (like agent providers).
    const badge = page.locator('.cost-badge');
    await expect(badge).toBeVisible({ timeout: 15000 });
    expect((await badge.textContent())?.trim()).toBe('$0.0002');
    await expect(badge).toHaveAttribute('title', 'Session cost');

    // The badge sits next to the Context meter in the chat form.
    await expect(page.locator('.context-usage')).toBeVisible();
  });
});
