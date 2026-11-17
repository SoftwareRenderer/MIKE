import { test, expect, type Page } from '@playwright/test';
import { chmodSync, existsSync, mkdirSync, rmSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { getProjectId, gotoProject, isolatedProject, ownProjectPath, selectProject } from './helpers';

// ============================================================
// The Explorer's context menu reports what a file is
// ============================================================
//
// A stat under the jail and a byte count the browser has never seen: only a real file in a real project
// puts those together. The row shapes — a gone path, a folder's floor, the refusal sentence — are held by
// `file-info-modal.unit.svelte.ts` and `handler_fs_stat_test.go`.
//
// Each test writes into the project its worker owns and takes its files out again, so the watcher finds no
// leftovers. It starts no turn and reads no config.

isolatedProject();

function projectDir(): string {
  const dir = ownProjectPath();
  if (!dir) throw new Error('this spec must call isolatedProject() to own a project directory');
  return dir;
}

/** Put one file in the project's own checkout, with the permissions its row will be asked to report. */
function write(rel: string, bytes: Buffer | string): void {
  const target = join(projectDir(), rel);
  mkdirSync(dirname(target), { recursive: true });
  writeFileSync(target, bytes);
  chmodSync(target, 0o644);
}

/** Open this project's Explorer and wait for its listing. */
async function openExplorer(page: Page): Promise<void> {
  await gotoProject(page);
  const projectId = await getProjectId(page);
  await selectProject(page, projectId);
  await expect(page.locator('.top-bar')).toBeVisible({ timeout: 10000 });
  await page.locator('.explorer-btn').click();
  await expect(page.locator('.file-browser-list')).toBeVisible({ timeout: 10000 });
}

async function rightClickAndChoose(page: Page, name: string, item: string): Promise<void> {
  const row = page.locator('.file-browser-item', { hasText: name }).first();
  await expect(row).toBeVisible({ timeout: 10000 });
  await row.click({ button: 'right' });
  const menu = page.locator('.file-context-menu');
  await expect(menu).toBeVisible();
  await menu.locator('[role="menuitem"]', { hasText: item }).click();
}

/**
 * The Info dialog. `role="dialog"` alone is never unique here — the branch view is one too, and it holds
 * the very listing whose row opened this dialog — so the dialog is picked out by the table only it draws.
 */
function infoDialog(page: Page) {
  return page.locator('[role="dialog"][aria-modal="true"]').filter({ has: page.locator('.fi-table') });
}

test.describe('Explorer file info', () => {
  test.describe.configure({ mode: 'serial' });

  const written: string[] = [];
  test.afterAll(() => {
    for (const rel of written.splice(0)) {
      const target = join(projectDir(), rel);
      if (existsSync(target)) rmSync(target, { recursive: true, force: true });
    }
  });

  test('a file reports its size, its permissions and when it changed', async ({ page }) => {
    written.push('info-file.txt');
    write('info-file.txt', 'nine byte');
    await openExplorer(page);
    await rightClickAndChoose(page, 'info-file.txt', 'Info');

    const dialog = infoDialog(page);
    await expect(dialog).toBeVisible();
    // One selection is titled by the thing it is about.
    await expect(dialog.locator('h2')).toHaveText('info-file.txt');
    await expect(dialog.locator('thead th')).toHaveText(['Name', 'Kind', 'Size', 'Modified']);

    const row = dialog.locator('tbody tr');
    await expect(row).toHaveCount(1);
    await expect(row.locator('.fi-name')).toHaveText('info-file.txt');
    await expect(row.locator('td').nth(1)).toHaveText('File');
    // The size is the server's stat, not anything the listing carried.
    await expect(row.locator('.fi-cell-size')).toHaveText('9 B');
    // A dialog about a moment must show one rather than an empty cell.
    await expect(row.locator('.fi-cell-date')).not.toBeEmpty();    await expect(row.locator('.fi-cell-name')).toContainText(join(projectDir(), 'info-file.txt'));
    await expect(row.locator('.fi-meta')).toContainText('0644');

    await dialog.getByRole('button', { name: 'Close', exact: true }).click();
    await expect(dialog).toHaveCount(0);
  });

  test('a folder reports how many things it holds and how big they are', async ({ page }) => {
    written.push('info-folder');
    write('info-folder/one.txt', '12345678');
    write('info-folder/nested/two.txt', '12345');
    await openExplorer(page);
    await rightClickAndChoose(page, 'info-folder', 'Info');

    const dialog = infoDialog(page);
    await expect(dialog).toBeVisible();
    const row = dialog.locator('tbody tr');
    await expect(row.locator('td').nth(1)).toHaveText('Folder');
    await expect(row.locator('.fi-cell-size')).toContainText('13 B');
    // one.txt, the nested folder, and the file inside it: the count is of entries, not of files.
    await expect(row.locator('.fi-cell-size')).toContainText('3 items');
    // The line above the table counts the selection.
    await expect(dialog.locator('.fi-summary')).toContainText('1 item');
    await expect(dialog.locator('.fi-summary')).toContainText('1 folder');
  });

  test('a selection is answered as rows under one line that counts them', async ({ page }) => {
    written.push('info-a.txt', 'info-b.txt');
    write('info-a.txt', 'aaa');
    write('info-b.txt', 'bbbb');
    await openExplorer(page);

    await page.locator('.file-browser-item', { hasText: 'info-a.txt' }).first().click();
    await page.locator('.file-browser-item', { hasText: 'info-b.txt' }).first().click({ modifiers: ['ControlOrMeta'] });
    await rightClickAndChoose(page, 'info-b.txt', 'Info');

    const dialog = infoDialog(page);
    await expect(dialog.locator('h2')).toHaveText('File info');
    await expect(dialog).toBeVisible();
    await expect(dialog.locator('tbody tr')).toHaveCount(2);
    await expect(dialog.locator('.fi-summary')).toContainText('2 items');
    // Counted by the server, not summed from anything the browser already held.
    await expect(dialog.locator('.fi-summary')).toContainText('7 B');
    // Rows travel in the order the selection was made.
    await expect(dialog.locator('tbody tr', { hasText: 'info-a.txt' }).locator('.fi-cell-size')).toHaveText('3 B');
    await expect(dialog.locator('tbody tr', { hasText: 'info-b.txt' }).locator('.fi-cell-size')).toHaveText('4 B');
  });
});
