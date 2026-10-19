import { test, expect, type Page } from '@playwright/test';
import { writeFileSync, readFileSync, rmSync } from 'node:fs';
import { join } from 'path';
import { apiGetJson, csrfFetch, getProjectId, gotoProject, isolatedProject, ownProjectPath } from './helpers';

// ============================================================
// The buffer a sandboxed project's editor left, read back after the board returns
// ============================================================
//
// Closing the explorer lands on the board, and the board sweeps IndexedDB of records whose file no longer
// belongs to a project. A sandboxed project's files are keyed by the container's mount rather than by the
// host checkout, so a sweep that knows only the host spelling deletes the buffer the reader just typed.

isolatedProject();

const BASE = '# Heading\n\nBody text\n';

/** The board's sweep runs once, on a board that holds a parent with a child. */
async function seedSubtask(page: Page, projectId: string): Promise<void> {
  const stamp = Date.now();
  const parent = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
    method: 'POST',
    body: { prompt: `sweep parent ${stamp}` },
  });
  expect(parent.ok).toBe(true);
  const child = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
    method: 'POST',
    body: { prompt: `sweep child ${stamp}`, parent_task_id: parent.data?.id },
  });
  expect(child.ok).toBe(true);
}

test('an unsaved buffer in a sandboxed project survives the board returning', async ({ page }) => {
  await gotoProject(page);
  await page.waitForSelector('.project-item', { timeout: 10000 });
  const projectId = await getProjectId(page);
  const projectDir = ownProjectPath();
  expect(projectDir, 'this worker has no own project path').toBeTruthy();
  const project = await apiGetJson<any>(page, `/api/projects/${projectId}`);

  const enabled = await csrfFetch(page, `/api/projects/${projectId}`, {
    method: 'PUT',
    body: { sandbox_enabled: true, sandbox_network_isolation: false },
  });
  expect(enabled.ok, `enabling the sandbox returned ${enabled.status}`).toBe(true);
  await seedSubtask(page, projectId);

  const file = `draft-sweep-${Date.now()}.md`;
  const abs = join(projectDir!, file);
  writeFileSync(abs, BASE);
  // The editor's own spelling of the file: the container's mount, not the host checkout.
  const mounted = `/home/sandbox/${project.name}/${file}`;

  try {
    await gotoProject(page, `/?project=${projectId}&view=explorer&file=${encodeURIComponent(mounted)}`);
    await expect(page.locator('.cm-editor')).toBeVisible({ timeout: 15000 });

    await page.locator('.cm-content').click();
    await page.keyboard.press('Control+End');
    await page.keyboard.type('\n\nTyped before closing.');
    await expect(page.locator('.editor-tab.unsaved')).toBeVisible();

    // Close the explorer, which is what puts the board back and runs its sweep.
    await page.locator('.explorer-header .close-btn').click();
    await expect(page.locator('.board')).toBeVisible({ timeout: 10000 });
    // The sweep is fire-and-forget, so it gets the moment it would take to erase the record.
    await page.waitForTimeout(2000);

    // Open the explorer again and reach the file through the tab that came back.
    await page.locator('.explorer-btn').click();
    await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });
    await page.locator('.editor-tab-name').filter({ hasText: file }).click();
    await expect.poll(async () => (await page.locator('.cm-content').first().textContent()) ?? '', { timeout: 20000 })
      .toContain('Typed before closing.');
    await expect(page.locator('.editor-tab.unsaved')).toBeVisible();

    // Nothing was written to the file on the way.
    expect(readFileSync(abs, 'utf8')).toBe(BASE);
  } finally {
    await csrfFetch(page, `/api/projects/${projectId}`, {
      method: 'PUT',
      body: { sandbox_enabled: false, sandbox_network_isolation: false },
    }).catch(() => {});
    try { rmSync(abs, { force: true }); } catch { /* ignore */ }
  }
});
