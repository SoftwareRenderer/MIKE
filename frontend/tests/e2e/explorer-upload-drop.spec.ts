import { test, expect, type Page } from '@playwright/test';
import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { getProjectId, gotoProject, isolatedProject, ownProjectPath, selectProject } from './helpers';

// ============================================================
// Dropping files onto the Explorer's file list uploads them
// ============================================================
//
// The browser half (drag events, the overlay, the overwrite question) and the
// server half (a binary-safe write into the project) only meet through a real
// Chromium and a real Go server, so this owns that seam. What happens *inside*
// either half — folder traversal, 409 handling, cancellation — is unit-tested.
//
// Each test writes into ITS OWN project directory (isolatedProject), because the
// upload is a real filesystem write into the browsed tree.

isolatedProject();

test.describe('Explorer upload by drop', () => {
  test.describe.configure({ mode: 'serial' });

  async function openExplorer(page: Page): Promise<string> {
    const projectId = await getProjectId(page);
    await selectProject(page, projectId);
    await expect(page.locator('.top-bar')).toBeVisible({ timeout: 10000 });
    await page.locator('.explorer-btn').click();
    await expect(page.locator('.file-browser-list')).toBeVisible({ timeout: 10000 });
    return projectId;
  }

  /** Dispatch a real drop of desktop files onto the listing. */
  async function dropOntoList(page: Page, files: Array<{ name: string; content: string }>) {
    const dataTransfer = await page.evaluateHandle((items) => {
      const dt = new DataTransfer();
      for (const item of items) dt.items.add(new File([item.content], item.name, { type: 'text/plain' }));
      return dt;
    }, files);
    await page.locator('.file-browser-list').dispatchEvent('dragover', { dataTransfer });
    await page.locator('.file-browser-list').dispatchEvent('drop', { dataTransfer });
  }

  const projectDir = () => {
    const dir = ownProjectPath();
    if (!dir) throw new Error('this spec must call isolatedProject() to own a project directory');
    return dir;
  };

  test('a file hovering the listing marks it as the drop target', async ({ page }) => {
    await gotoProject(page);
    await openExplorer(page);

    const dataTransfer = await page.evaluateHandle(() => {
      const dt = new DataTransfer();
      dt.items.add(new File(['x'], 'hover-only.txt', { type: 'text/plain' }));
      return dt;
    });
    const list = page.locator('.file-browser-list');
    await list.dispatchEvent('dragenter', { dataTransfer });
    await expect(list).toHaveClass(/is-drop-target/);

    await list.dispatchEvent('dragleave', { dataTransfer });
    await expect(list).not.toHaveClass(/is-drop-target/);
  });

  test('a dropped file is written into the project and appears in the listing', async ({ page }) => {
    await gotoProject(page);
    await openExplorer(page);

    const name = `dropped-${Date.now()}.txt`;
    const target = join(projectDir(), name);
    try {
      await dropOntoList(page, [{ name, content: 'dropped from the desktop\n' }]);

      await expect(page.locator('.file-browser-item', { hasText: name })).toBeVisible({ timeout: 10000 });
      await expect(page.locator('.toast')).toContainText('Uploaded 1 file', { timeout: 10000 });
      await expect.poll(() => (existsSync(target) ? readFileSync(target, 'utf8') : null)).toBe('dropped from the desktop\n');
    } finally {
      // The listing's own watcher keeps noticing leftovers; take the file out of
      // the repo so the next test's git status is not a pile of untracked drops.
      if (existsSync(target)) writeFileSync(target, '');
    }
  });

  test('dropping a name that already exists asks first, and a refusal changes nothing', async ({ page }) => {
    const name = `replace-me-${Date.now()}.txt`;
    const target = join(projectDir(), name);
    writeFileSync(target, 'original\n');

    await gotoProject(page);
    await openExplorer(page);
    await expect(page.locator('.file-browser-item', { hasText: name })).toBeVisible({ timeout: 10000 });

    await dropOntoList(page, [{ name, content: 'replacement\n' }]);

    const question = page.locator('[role="dialog"]').filter({ hasText: 'Replace existing files?' });
    await expect(question).toBeVisible({ timeout: 10000 });
    await expect(question.locator('.confirm-list li')).toHaveText(name);
    await question.locator('.btn-cancel').click();

    await expect.poll(() => readFileSync(target, 'utf8')).toBe('original\n');
    await expect(page.locator('.toast')).toContainText('Upload cancelled', { timeout: 10000 });
  });

  test('confirming the question replaces the file', async ({ page }) => {
    const name = `replace-ok-${Date.now()}.txt`;
    const target = join(projectDir(), name);
    writeFileSync(target, 'original\n');

    await gotoProject(page);
    await openExplorer(page);
    await expect(page.locator('.file-browser-item', { hasText: name })).toBeVisible({ timeout: 10000 });

    await dropOntoList(page, [{ name, content: 'replacement\n' }]);

    const question = page.locator('[role="dialog"]').filter({ hasText: 'Replace existing files?' });
    await expect(question).toBeVisible({ timeout: 10000 });
    // The button names the action, so the dialog cannot be read as "delete".
    await expect(question.locator('.btn-confirm')).toHaveText('Replace');
    await question.locator('.btn-confirm').click();

    await expect.poll(() => readFileSync(target, 'utf8')).toBe('replacement\n');
    await expect(page.locator('.toast')).toContainText('Uploaded 1 file', { timeout: 10000 });
  });

  test('the Upload button is the same upload without a drag', async ({ page }) => {
    await gotoProject(page);
    await openExplorer(page);

    const name = `picked-${Date.now()}.txt`;
    const target = join(projectDir(), name);
    // The chooser the button opens is what a phone's file sheet and a keyboard both
    // use — a drop needs a desktop drag source, which neither has.
    const chooser = page.waitForEvent('filechooser');
    await page.locator('.upload-btn').click();
    (await chooser).setFiles({ name, mimeType: 'text/plain', buffer: Buffer.from('picked, not dropped\n') });

    await expect(page.locator('.file-browser-item', { hasText: name })).toBeVisible({ timeout: 10000 });
    await expect(page.locator('.toast')).toContainText('Uploaded 1 file', { timeout: 10000 });
    await expect.poll(() => (existsSync(target) ? readFileSync(target, 'utf8') : null)).toBe('picked, not dropped\n');
  });

  test('the narrow layout keeps an upload path, because there is nothing to drag', async ({ page }) => {
    // Navigate at desktop width first: below the breakpoint the sidebar is behind a
    // menu button, so the project row is unclickable — and the explorer must be open
    // before the resize, since that is what the tab bar belongs to.
    await gotoProject(page);
    await selectProject(page, await getProjectId(page));
    await page.locator('.explorer-btn').click();
    await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });

    await page.setViewportSize({ width: 390, height: 844 });
    await expect(page.locator('.mobile-tab-bar')).toBeVisible({ timeout: 10000 });

    // The files tab is first, and it is what the explorer opens on.
    await page.locator('.mobile-tab-bar .tab-btn').first().click();
    await expect(page.locator('.file-browser-list')).toBeVisible({ timeout: 10000 });
    await expect(page.locator('.upload-btn')).toBeVisible();

    // The overlay's Cancel is only on screen mid-upload, so measure the cascade on a
    // node that carries the same markup position: at this width it must be a real tap
    // target, not the 20px control the desktop layout uses.
    const cancelStyle = await page.evaluate(() => {
      const list = document.createElement('div');
      list.className = 'file-browser-list';
      const box = document.createElement('div');
      box.className = 'drop-progress';
      const button = document.createElement('button');
      button.className = 'drop-cancel-btn';
      box.appendChild(button);
      list.appendChild(box);
      document.body.appendChild(list);
      const computed = getComputedStyle(button);
      const read = {
        minHeight: parseFloat(computed.minHeight),
        fontSize: parseFloat(computed.fontSize),
        padding: computed.padding,
        // rem resolves against the root, which a browser's font setting can move —
        // so the expectation is computed from it rather than hardcoded to 16px.
        root: parseFloat(getComputedStyle(document.documentElement).fontSize),
      };
      list.remove();
      return read;
    });
    expect(cancelStyle.minHeight).toBe(32);
    expect(cancelStyle.fontSize).toBeCloseTo(cancelStyle.root * 0.8125, 1);
    expect(cancelStyle.padding).toBe('6px 10px');
  });
});
