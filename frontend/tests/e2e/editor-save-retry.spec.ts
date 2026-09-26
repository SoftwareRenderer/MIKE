import { test, expect, type Page } from '@playwright/test';
import { readFileSync, writeFileSync, rmSync } from 'node:fs';
import { join } from 'path';
import { getProjectId, projectDirPath, selectProject, isolatedProject, gotoProject } from './helpers';

// ============================================================
// A file save the connection lost
// ============================================================
//
// The retry against the real server is what needs a browser here; the wait's own policy is held by
// `src/lib/components/__tests__/editor-save-failure.unit.svelte.ts`.

isolatedProject();

test.describe('A save the connection lost', () => {
  test.describe.configure({ mode: 'serial' });

  test.beforeEach(async ({ page }) => {
    test.setTimeout(45000);
    await gotoProject(page);
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  async function openFile(page: Page, projectId: string, file: string) {
    await selectProject(page, projectId);
    await page.waitForSelector('.top-bar', { timeout: 10000 });
    await page.locator('.explorer-btn').click();
    await expect(page.locator('.file-browser-list')).toBeVisible({ timeout: 10000 });
    await page.locator('.file-browser-item', { hasText: file }).click();
    await expect(page.locator('.cm-editor')).toBeVisible({ timeout: 10000 });
  }

  test('keeps the buffer on screen and writes itself once the request can land', async ({ page }) => {
    const projectId = await getProjectId(page);
    const projectDir = await projectDirPath(page, projectId);
    const file = `save-blip-${Date.now()}.txt`;
    const absFile = join(projectDir, file);
    writeFileSync(absFile, 'alpha\nbeta\n');

    // The first write is the blip; every later one goes through, so the retry is what succeeds.
    let writeAttempts = 0;
    await page.route('**/api/fs/write*', async (route) => {
      writeAttempts++;
      if (writeAttempts === 1) return route.abort('failed');
      return route.fallback();
    });

    try {
      await openFile(page, projectId, file);
      await expect(page.locator('.cm-content')).toContainText('beta');

      await page.locator('.cm-content').click();
      await page.keyboard.press('End');
      await page.keyboard.type(' — edited while the network was flaky');
      await expect(page.locator('.editor-tab.unsaved')).toHaveCount(1);
      await expect(page.locator('.editor-save-btn')).toBeEnabled();

      await page.locator('.editor-save-btn').click();

      // `.editor-error` replaces `.cm-editor`, so a save that reported itself there would delete the text
      // being edited. Nothing is said at all: the unsaved marker is the feedback.
      await expect.poll(() => writeAttempts).toBe(1);
      await expect(page.locator('.editor-error')).toHaveCount(0);
      await expect(page.locator('.cm-content')).toContainText('edited while the network was flaky');
      await expect(page.locator('.toast')).toHaveCount(0);
      await expect(page.locator('.editor-tab.unsaved')).toHaveCount(1);
      await expect(page.locator('.editor-save-btn')).toBeEnabled();

      // The retry carries it to the server, once: a second write would duplicate bytes already on disk.
      await expect
        .poll(() => readFileSync(absFile, 'utf8'))
        .toContain('edited while the network was flaky');
      await expect(page.locator('.editor-tab.unsaved')).toHaveCount(0);
      expect(writeAttempts).toBe(2);
    } finally {
      try { rmSync(absFile, { force: true }); } catch { /* ignore */ }
    }
  });
});
