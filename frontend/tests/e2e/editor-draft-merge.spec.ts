import { test, expect, type Page } from '@playwright/test';
import { writeFileSync, readFileSync, rmSync } from 'node:fs';
import { join } from 'path';
import { getProjectId, projectDirPath, selectProject, isolatedProject, gotoProject } from './helpers';

// ============================================================
// An unsaved draft merged with the bytes the file holds now
// ============================================================
//
// A draft outlives its tab, so the file can be rewritten between the last keystroke and the next open, and
// the open has to carry both.

isolatedProject();

const BASE = 'Line 1: alpha\nLine 2: beta\nLine 3: gamma\n';

test.describe.configure({ mode: 'serial' });

async function openFile(page: Page, projectId: string, file: string) {
  await selectProject(page, projectId);
  await page.waitForSelector('.top-bar', { timeout: 10000 });
  await page.locator('.explorer-btn').click();
  await expect(page.locator('.file-browser-item', { hasText: file })).toBeVisible({ timeout: 10000 });
  await page.locator('.file-browser-item', { hasText: file }).click();
  await expect(page.locator('.cm-editor')).toBeVisible({ timeout: 10000 });
}

/** Leave the project: the editor unmounts, which is what flushes the draft it holds. */
async function leaveProject(page: Page) {
  await gotoProject(page, '/');
  await expect(page.locator('.board')).toBeVisible({ timeout: 10000 });
}

/** Type at the end of line 1, so the caret is where the edit is meant to be. */
async function editFirstLine(page: Page, suffix: string) {
  await page.locator('.cm-content').first().click();
  await page.keyboard.press('Control+Home');
  await page.keyboard.press('End');
  await page.keyboard.type(suffix);
}

/** Wait until IndexedDB holds this buffer for this file. */
async function waitForDraft(page: Page, path: string, text: string, base: string) {
  await expect.poll(async () => page.evaluate((p) => new Promise<{ text?: string; base?: string }>((resolve) => {
    const req = indexedDB.open('kanban-storage');
    req.onsuccess = () => {
      const store = req.result.transaction('editor-drafts', 'readonly').objectStore('editor-drafts');
      const get = store.get(p);
      get.onsuccess = () => resolve(get.result ?? {});
      get.onerror = () => resolve({});
    };
    req.onerror = () => resolve({});
  }), path), { timeout: 20000 }).toMatchObject({ text, base });
}

const bufferText = async (page: Page) => (await page.locator('.cm-content').first().textContent()) ?? '';

test('a draft and a change to another line both reach the buffer, unsaid', async ({ page }) => {
  test.setTimeout(60000);
  await gotoProject(page);
  await page.waitForSelector('.project-item', { timeout: 10000 });
  const projectId = await getProjectId(page);
  const projectDir = await projectDirPath(page, projectId);
  const file = `draft-merge-${Date.now()}.txt`;
  const absFile = join(projectDir, file);

  try {
    writeFileSync(absFile, BASE);
    await openFile(page, projectId, file);
    await editFirstLine(page, ' USER');
    await waitForDraft(page, absFile, 'Line 1: alpha USER\nLine 2: beta\nLine 3: gamma\n', BASE);

    await leaveProject(page);
    writeFileSync(absFile, 'Line 1: alpha\nLine 2: beta\nLine 3: gamma EXTERNAL\n');

    await openFile(page, projectId, file);
    await expect.poll(async () => bufferText(page), { timeout: 20000 })
      .toContain('Line 1: alpha USER');
    const buffer = await bufferText(page);
    expect(buffer).toContain('gamma EXTERNAL');

    // A merge that took both sides owes the reader no message.
    await expect(page.locator('.toast', { hasText: 'unsaved' })).toHaveCount(0);
  } finally {
    try { rmSync(absFile, { force: true }); } catch { /* ignore */ }
  }
});

test('a draft and a change to the same line are both left in the file to resolve', async ({ page }) => {
  test.setTimeout(60000);
  await gotoProject(page);
  await page.waitForSelector('.project-item', { timeout: 10000 });
  const projectId = await getProjectId(page);
  const projectDir = await projectDirPath(page, projectId);
  const file = `draft-conflict-${Date.now()}.txt`;
  const absFile = join(projectDir, file);
  const external = 'Line 1: alpha EXTERNAL\nLine 2: beta\nLine 3: gamma\n';

  try {
    writeFileSync(absFile, BASE);
    await openFile(page, projectId, file);
    await editFirstLine(page, ' USER');
    await waitForDraft(page, absFile, 'Line 1: alpha USER\nLine 2: beta\nLine 3: gamma\n', BASE);

    await leaveProject(page);
    writeFileSync(absFile, external);

    await openFile(page, projectId, file);
    await expect.poll(async () => bufferText(page), { timeout: 20000 }).toContain('alpha USER');
    const buffer = await bufferText(page);
    expect(buffer).toContain('alpha EXTERNAL');
    await expect(page.locator('.conflict-banner')).toBeVisible();

    // Saving now would write the markers into the file, so it asks first — and a "no" writes nothing.
    await page.locator('.cm-content').first().click();
    await page.keyboard.press('Control+s');
    const dialog = page.locator('[role="dialog"]', { hasText: 'Save conflict markers?' });
    await expect(dialog).toBeVisible({ timeout: 10000 });
    await dialog.locator('.btn-cancel').click();
    await expect(dialog).not.toBeVisible({ timeout: 10000 });
    expect(readFileSync(absFile, 'utf8')).toBe(external);
  } finally {
    try { rmSync(absFile, { force: true }); } catch { /* ignore */ }
  }
});
