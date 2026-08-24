import { test, expect } from '@playwright/test';
import { getProjectId, csrfFetch, navigateToTask, createTaskViaApi } from './helpers';

// ============================================================
// TASK VIEW MOBILE TAB BAR (ICON-ONLY TABS)
//
// On mobile (≤768px), the Task View shows a tab bar above the
// columns: Board, Agent, (Checkpoint), Changes. Board/Agent/
// Changes are icon-only (no text labels); Checkpoint keeps its
// label. Icons are 20×20. Each icon-only button carries an
// aria-label (matching its title) for accessibility, and the
// tabs remain functional.
// ============================================================

test.describe('Task view mobile tab bar', () => {
  test.describe.configure({ mode: 'serial' });

  test('icon-only tabs with 20px icons remain functional', async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    const projectId = await getProjectId(page);
    const ts = Date.now();
    const taskId = await createTaskViaApi(page, projectId, `Mobile tab bar ${ts}`);

    try {
      await navigateToTask(page, taskId, projectId);

      // Mobile viewport (repo convention for mobile specs)
      await page.setViewportSize({ width: 390, height: 844 });

      const bar = page.locator('.mobile-tab-bar');
      await expect(bar).toBeVisible({ timeout: 10000 });

      // --- Board/Agent/Changes are icon-only: no text label ---
      for (const label of ['Board overview', 'Agent chat', 'Working changes']) {
        const btn = page.locator(`.mobile-tab-bar .tab-btn[aria-label="${label}"]`);
        await expect(btn).toHaveCount(1);
        const text = (await btn.innerText()).trim();
        expect(text, `tab "${label}" must be icon-only, got: ${JSON.stringify(text)}`).toBe('');
      }

      // --- Checkpoint tab is not rendered by default ---
      await expect(page.locator('.mobile-tab-bar .tab-btn[title="Checkpoint changes"]')).toHaveCount(0);

      // --- All tab icons are 20×20 ---
      const svgs = page.locator('.mobile-tab-bar .tab-btn svg');
      await expect(svgs).toHaveCount(3);
      for (let i = 0; i < (await svgs.count()); i++) {
        const svg = svgs.nth(i);
        expect(await svg.getAttribute('width')).toBe('20');
        expect(await svg.getAttribute('height')).toBe('20');
      }

      // --- Tabs remain functional (click via aria-label) ---
      // Board → board overview
      await page.locator('.mobile-tab-bar .tab-btn[aria-label="Board overview"]').click();
      await expect(page.locator('.column-context')).toBeVisible();

      // Agent → chat
      await page.locator('.mobile-tab-bar .tab-btn[aria-label="Agent chat"]').click();
      await expect(page.locator('.chat-form, .btn-start-task')).toBeVisible({ timeout: 10000 });

      // Changes → diff panel
      await page.locator('.mobile-tab-bar .tab-btn[aria-label="Working changes"]').click();
      await expect(page.locator('.diff-content')).toBeVisible({ timeout: 10000 });
    } finally {
      await page.setViewportSize({ width: 1280, height: 720 });
      await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}`, { method: 'DELETE' });
    }
  });
});
