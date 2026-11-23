import { test, expect } from '@playwright/test';
import { writeFileSync, readFileSync, rmSync, readdirSync, statSync, mkdirSync, existsSync } from 'node:fs';
import { execSync } from 'node:child_process';
import { join } from 'path';
import { apiGetJson, csrfFetch, getProjectId, gitIn, isolatedProject, gotoProject, ownProjectPath, selectProject } from './helpers';

// Each test file gets its own project: see isolatedProject() in helpers.ts.
isolatedProject();


test.describe.configure({ mode: 'parallel' });

// `ownProjectPath()` and `getProjectId()` name this worker's own project, from global setup. An empty one is
// the harness failing, not a condition a tab test can proceed without.

test.describe('Editor Tabs', () => {
  test.beforeEach(async ({ page }) => {
    test.setTimeout(30000);
    await gotoProject(page);
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should open multiple files as separate tabs', async ({ page }) => {
    const projectId = await getProjectId(page);
    // The project explorer lists the project directory, so that is where the fixture
    // files have to be written — not a task worktree.
    const projectDir = ownProjectPath();
    expect(projectDir, 'this worker has no own project path').toBeTruthy();
    expect(projectId, 'this worker has no own project id').toBeTruthy();

    const uniqueId = Date.now();
    const file1 = `tab-test-file1-${uniqueId}.txt`;
    const file2 = `tab-test-file2-${uniqueId}.txt`;

    writeFileSync(join(projectDir, file1), 'Content of file 1\nLine 2\nLine 3\n');
    writeFileSync(join(projectDir, file2), 'Content of file 2\nDifferent content\n');

    try {
      // Open explorer
      await selectProject(page, projectId);
      await page.waitForSelector('.top-bar', { timeout: 10000 });
      await page.locator('.explorer-btn').click();
      await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });
      await expect(page.locator('.file-browser-list')).toBeVisible({ timeout: 10000 });

      // Pin the first file: a plain click opens a preview tab that the next click
      // replaces, and this test is about SEPARATE tabs.
      await page.locator('.file-browser-item', { hasText: file1 }).click();
      await expect(page.locator('.editor-tab-name').filter({ hasText: file1 })).toBeVisible({ timeout: 10000 });
      await page.locator('.file-browser-item', { hasText: file1 }).dblclick();
      await expect(page.locator('.cm-editor')).toBeVisible({ timeout: 10000 });

      // Verify first tab exists
      await expect(page.locator('.editor-tab')).toHaveCount(1);
      await expect(page.locator('.editor-tab-name')).toContainText(file1);

      // Open second file
      await page.locator('.file-browser-item', { hasText: file2 }).click();

      // Should now have 2 tabs
      await expect(page.locator('.editor-tab')).toHaveCount(2);

      // Both tab names should be visible
      const tabNames = page.locator('.editor-tab-name');
      await expect(tabNames).toHaveCount(2);

      // Second file should be active (last opened)
      const activeTab = page.locator('.editor-tab.active');
      await expect(activeTab.locator('.editor-tab-name')).toContainText(file2);

      // Click first tab to switch
      await tabNames.filter({ hasText: file1 }).click();

      // First file should now be active
      await expect(page.locator('.editor-tab.active .editor-tab-name')).toContainText(file1);

      // Verify CodeMirror shows file 1 content (active tab's editor is visible)
      await expect(page.locator('.cm-editor').first()).toBeVisible();
    } finally {
      try {
        rmSync(join(projectDir, file1), { force: true });
        rmSync(join(projectDir, file2), { force: true });
      } catch { /* ignore */ }
    }
  });

  test('should close a tab and switch to adjacent tab', async ({ page }) => {
    const projectId = await getProjectId(page);
    // The project explorer lists the project directory, so that is where the fixture
    // files have to be written — not a task worktree.
    const projectDir = ownProjectPath();
    expect(projectDir, 'this worker has no own project path').toBeTruthy();
    expect(projectId, 'this worker has no own project id').toBeTruthy();

    const uniqueId = Date.now();
    const file1 = `tab-close-file1-${uniqueId}.txt`;
    const file2 = `tab-close-file2-${uniqueId}.txt`;
    const file3 = `tab-close-file3-${uniqueId}.txt`;

    writeFileSync(join(projectDir, file1), 'File 1 content\n');
    writeFileSync(join(projectDir, file2), 'File 2 content\n');
    writeFileSync(join(projectDir, file3), 'File 3 content\n');

    try {
      // Open explorer
      await selectProject(page, projectId);
      await page.waitForSelector('.top-bar', { timeout: 10000 });
      await page.locator('.explorer-btn').click();
      await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });
      await expect(page.locator('.file-browser-list')).toBeVisible({ timeout: 10000 });

      // Pin file1 and file2; file3 may stay a preview tab.
      await page.locator('.file-browser-item', { hasText: file1 }).click();
      await expect(page.locator('.editor-tab-name').filter({ hasText: file1 })).toBeVisible({ timeout: 10000 });
      await page.locator('.file-browser-item', { hasText: file1 }).dblclick();
      await expect(page.locator('.cm-editor')).toBeVisible({ timeout: 10000 });
      await page.locator('.file-browser-item', { hasText: file2 }).click();
      await expect(page.locator('.editor-tab-name').filter({ hasText: file2 })).toBeVisible({ timeout: 10000 });
      await page.locator('.file-browser-item', { hasText: file2 }).dblclick();
      await page.locator('.file-browser-item', { hasText: file3 }).click();

      await expect(page.locator('.editor-tab')).toHaveCount(3);

      // Close the middle tab (file2)
      const tabs = page.locator('.editor-tab');
      const middleTab = tabs.nth(1);
      await middleTab.locator('.btn-close-tab').click();

      // Should now have 2 tabs
      await expect(page.locator('.editor-tab')).toHaveCount(2);

      // file2 should no longer be in tabs
      const tabNames = page.locator('.editor-tab-name');
      const names = await tabNames.allTextContents();
      expect(names).not.toContain(file2);
      expect(names).toContain(file1);
      expect(names).toContain(file3);
    } finally {
      try {
        rmSync(join(projectDir, file1), { force: true });
        rmSync(join(projectDir, file2), { force: true });
        rmSync(join(projectDir, file3), { force: true });
      } catch { /* ignore */ }
    }
  });

  test('should close last tab and show placeholder', async ({ page }) => {
    const projectId = await getProjectId(page);
    // The project explorer lists the project directory, so that is where the fixture
    // files have to be written — not a task worktree.
    const projectDir = ownProjectPath();
    expect(projectDir, 'this worker has no own project path').toBeTruthy();
    expect(projectId, 'this worker has no own project id').toBeTruthy();

    const uniqueId = Date.now();
    const file1 = `tab-last-${uniqueId}.txt`;
    writeFileSync(join(projectDir, file1), 'Solo file content\n');

    try {
      // Open explorer
      await selectProject(page, projectId);
      await page.waitForSelector('.top-bar', { timeout: 10000 });
      await page.locator('.explorer-btn').click();
      await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });
      await expect(page.locator('.file-browser-list')).toBeVisible({ timeout: 10000 });

      // Open the file
      await page.locator('.file-browser-item', { hasText: file1 }).click();
      await expect(page.locator('.cm-editor')).toBeVisible({ timeout: 10000 });
      await expect(page.locator('.editor-tab')).toHaveCount(1);

      // Close the only tab
      await page.locator('.btn-close-tab').click();

      // Should show placeholder (no tabs, no editor)
      await expect(page.locator('.editor-tab')).toHaveCount(0);
      await expect(page.locator('.file-editor-placeholder')).toBeVisible();
    } finally {
      try {
        rmSync(join(projectDir, file1), { force: true });
      } catch { /* ignore */ }
    }
  });

  test('should re-open an already-open file and switch to its tab', async ({ page }) => {
    const projectId = await getProjectId(page);
    // The project explorer lists the project directory, so that is where the fixture
    // files have to be written — not a task worktree.
    const projectDir = ownProjectPath();
    expect(projectDir, 'this worker has no own project path').toBeTruthy();
    expect(projectId, 'this worker has no own project id').toBeTruthy();

    const uniqueId = Date.now();
    const file1 = `tab-reopen-${uniqueId}a.txt`;
    const file2 = `tab-reopen-${uniqueId}b.txt`;

    writeFileSync(join(projectDir, file1), 'File 1\n');
    writeFileSync(join(projectDir, file2), 'File 2\n');

    try {
      // Open explorer
      await selectProject(page, projectId);
      await page.waitForSelector('.top-bar', { timeout: 10000 });
      await page.locator('.explorer-btn').click();
      await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });
      await expect(page.locator('.file-browser-list')).toBeVisible({ timeout: 10000 });

      // Open file1 pinned, so file2 gets its own tab
      await page.locator('.file-browser-item', { hasText: file1 }).click();
      await expect(page.locator('.editor-tab-name').filter({ hasText: file1 })).toBeVisible({ timeout: 10000 });
      await page.locator('.file-browser-item', { hasText: file1 }).dblclick();
      await expect(page.locator('.cm-editor')).toBeVisible({ timeout: 10000 });

      // Open file2
      await page.locator('.file-browser-item', { hasText: file2 }).click();
      await expect(page.locator('.editor-tab')).toHaveCount(2);

      // Click file1 again in the file browser: it is already open, so this must
      // switch to its tab rather than open a third one.
      await page.locator('.file-browser-item', { hasText: file1 }).click();

      // Should still have 2 tabs (no duplicate)
      await expect(page.locator('.editor-tab')).toHaveCount(2);

      // file1 should be active
      await expect(page.locator('.editor-tab.active .editor-tab-name')).toContainText(file1);
    } finally {
      try {
        rmSync(join(projectDir, file1), { force: true });
        rmSync(join(projectDir, file2), { force: true });
      } catch { /* ignore */ }
    }
  });

  test('should open new files in preview mode (italicized) and replace preview tab', async ({ page }) => {
    const projectId = await getProjectId(page);
    // The project explorer lists the project directory, so that is where the fixture
    // files have to be written — not a task worktree.
    const projectDir = ownProjectPath();
    expect(projectDir, 'this worker has no own project path').toBeTruthy();
    expect(projectId, 'this worker has no own project id').toBeTruthy();

    const uniqueId = Date.now();
    const file1 = `tab-preview-${uniqueId}a.txt`;
    const file2 = `tab-preview-${uniqueId}b.txt`;

    writeFileSync(join(projectDir, file1), 'Preview file 1\n');
    writeFileSync(join(projectDir, file2), 'Preview file 2\n');

    try {
      // Open explorer
      await selectProject(page, projectId);
      await page.waitForSelector('.top-bar', { timeout: 10000 });
      await page.locator('.explorer-btn').click();
      await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });
      await expect(page.locator('.file-browser-list')).toBeVisible({ timeout: 10000 });

      // Open file1 (single click = preview mode)
      await page.locator('.file-browser-item', { hasText: file1 }).click();
      await expect(page.locator('.cm-editor')).toBeVisible({ timeout: 10000 });

      // Tab should be in preview mode (italicized)
      await expect(page.locator('.editor-tab-name.preview')).toContainText(file1);

      // Open file2 (single click = replaces preview tab)
      await page.locator('.file-browser-item', { hasText: file2 }).click();

      // Should still have only 1 tab (preview was replaced)
      await expect(page.locator('.editor-tab')).toHaveCount(1);

      // Tab should show file2 in preview mode
      await expect(page.locator('.editor-tab-name.preview')).toContainText(file2);

      // file1 tab should be gone
      const tabNames = await page.locator('.editor-tab-name').allTextContents();
      expect(tabNames).not.toContain(file1);
    } finally {
      try {
        rmSync(join(projectDir, file1), { force: true });
        rmSync(join(projectDir, file2), { force: true });
      } catch { /* ignore */ }
    }
  });

  test('should promote preview tab to pinned on double-click', async ({ page }) => {
    const projectId = await getProjectId(page);
    // The project explorer lists the project directory, so that is where the fixture
    // files have to be written — not a task worktree.
    const projectDir = ownProjectPath();
    expect(projectDir, 'this worker has no own project path').toBeTruthy();
    expect(projectId, 'this worker has no own project id').toBeTruthy();

    const uniqueId = Date.now();
    const file1 = `tab-pin-${uniqueId}a.txt`;
    const file2 = `tab-pin-${uniqueId}b.txt`;

    writeFileSync(join(projectDir, file1), 'Pin file 1\n');
    writeFileSync(join(projectDir, file2), 'Pin file 2\n');

    try {
      // Open explorer
      await selectProject(page, projectId);
      await page.waitForSelector('.top-bar', { timeout: 10000 });
      await page.locator('.explorer-btn').click();
      await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });
      await expect(page.locator('.file-browser-list')).toBeVisible({ timeout: 10000 });

      // Open file1 in preview mode (single click)
      await page.locator('.file-browser-item', { hasText: file1 }).click();
      await expect(page.locator('.cm-editor')).toBeVisible({ timeout: 10000 });

      // Tab should be in preview mode
      await expect(page.locator('.editor-tab-name.preview')).toContainText(file1);

      // Double-click the file in the file browser to pin it
      await page.locator('.file-browser-item', { hasText: file1 }).dblclick();

      // Tab should no longer be in preview mode (promoted to pinned)
      await expect(page.locator('.editor-tab-name.preview')).not.toBeVisible({ timeout: 2000 });
      await expect(page.locator('.editor-tab-name')).toContainText(file1);

      // Open file2 — should create a NEW tab (not replace the pinned one)
      await page.locator('.file-browser-item', { hasText: file2 }).click();

      // Should now have 2 tabs
      await expect(page.locator('.editor-tab')).toHaveCount(2);

      // Both files should be present
      const tabNames = await page.locator('.editor-tab-name').allTextContents();
      expect(tabNames).toContain(file1);
      expect(tabNames).toContain(file2);
    } finally {
      try {
        rmSync(join(projectDir, file1), { force: true });
        rmSync(join(projectDir, file2), { force: true });
      } catch { /* ignore */ }
    }
  });

  test('closing a tab keeps its unsaved text, and asks nothing', async ({ page }) => {
    const projectId = await getProjectId(page);
    // The project explorer lists the project directory, so that is where the fixture
    // files have to be written — not a task worktree.
    const projectDir = ownProjectPath();
    expect(projectDir, 'this worker has no own project path').toBeTruthy();
    expect(projectId, 'this worker has no own project id').toBeTruthy();

    const uniqueId = Date.now();
    const file1 = `tab-unsaved-close-${uniqueId}.txt`;
    const abs = join(projectDir, file1);
    writeFileSync(abs, 'Original content\n');

    try {
      await selectProject(page, projectId);
      await page.waitForSelector('.top-bar', { timeout: 10000 });
      await page.locator('.explorer-btn').click();
      await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });
      await expect(page.locator('.file-browser-list')).toBeVisible({ timeout: 10000 });

      await page.locator('.file-browser-item', { hasText: file1 }).click();
      await expect(page.locator('.cm-editor')).toBeVisible({ timeout: 10000 });

      await page.locator('.cm-content').click();
      await page.keyboard.press('End');
      await page.keyboard.type(' Modified');
      await expect(page.locator('.editor-tab.unsaved')).toBeVisible();

      // Closing costs nothing, so nothing is asked: the buffer is parked, not thrown away.
      await page.locator('.btn-close-tab').click();
      await expect(page.locator('.editor-tab')).toHaveCount(0);
      await expect(page.locator('.overlay')).toHaveCount(0);

      // The file on disk is untouched, and reopening reads the parked buffer back.
      expect(readFileSync(abs, 'utf8')).toBe('Original content\n');
      await page.locator('.file-browser-item', { hasText: file1 }).click();
      await expect(page.locator('.cm-content')).toContainText('Original content Modified');
      await expect(page.locator('.editor-tab.unsaved')).toBeVisible();
    } finally {
      try {
        rmSync(abs, { force: true });
      } catch { /* ignore */ }
    }
  });

  // `isolatedProject()` gives this worker one project, so the second is added over the API and deleted
  // with the test. What must not survive a switch is the address naming a file the project it left held.
  test('names only a file of the project on screen', async ({ page }) => {
    test.setTimeout(45000);
    const ownId = await getProjectId(page);
    const ownDir = ownProjectPath();
    expect(ownDir, 'this worker has no own project path').toBeTruthy();

    const stamp = Date.now();
    const ownFile = `switch-alpha-${stamp}.txt`;
    const otherFile = `switch-bravo-${stamp}.txt`;
    const otherDir = `/tmp/kanban-e2e-tabs-switch-${stamp}`;
    writeFileSync(join(ownDir!, ownFile), 'Alpha\n');
    mkdirSync(otherDir, { recursive: true });
    writeFileSync(join(otherDir, otherFile), 'Bravo\n');
    execSync('git init -q', { cwd: otherDir });
    execSync('git add -A', { cwd: otherDir });
    execSync('git -c user.email="e2e@mike.local" -c user.name="e2e" commit -qm seed', { cwd: otherDir });

    try {
      await csrfFetch(page, '/api/projects', { method: 'POST', body: { path: otherDir } });
      let otherId = '';
      await expect(async () => {
        otherId = (await apiGetJson<any[]>(page, '/api/projects')).find((p: any) => p.path === otherDir)?.id || '';
        expect(otherId).toBeTruthy();
      }).toPass({ timeout: 30000 });
      // The sidebar lists what it fetched, so pick the new row up before reaching for it.
      await gotoProject(page);
      await page.waitForSelector('.project-item', { timeout: 10000 });

      await selectProject(page, ownId);
      await page.locator('.explorer-btn').click();
      await expect(page.locator('.file-browser-list')).toBeVisible({ timeout: 10000 });
      await page.locator('.file-browser-item', { hasText: ownFile }).dblclick();
      await expect(page.locator('.editor-tab-name').filter({ hasText: ownFile })).toBeVisible({ timeout: 10000 });
      await expect.poll(() => page.url()).toContain(ownFile);

      // The switch: the explorer stays on screen, holding another project's files.
      await selectProject(page, otherId);
      await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });
      await expect(page.locator('.editor-tab')).toHaveCount(0);
      await expect.poll(() => page.url()).not.toContain(ownFile);

      // Each project shows and names its own file.
      await page.locator('.file-browser-item', { hasText: otherFile }).dblclick();
      await expect(page.locator('.editor-tab-name').filter({ hasText: otherFile })).toBeVisible({ timeout: 10000 });
      await selectProject(page, ownId);
      await expect(page.locator('.editor-tab-name').filter({ hasText: ownFile })).toBeVisible({ timeout: 10000 });
      await expect(page.locator('.editor-tab-name').filter({ hasText: otherFile })).toHaveCount(0);
      await expect.poll(() => page.url()).toContain(ownFile);
      await selectProject(page, otherId);
      await expect(page.locator('.editor-tab-name').filter({ hasText: otherFile })).toBeVisible({ timeout: 10000 });
      await expect.poll(() => page.url()).toContain(otherFile);
    } finally {
      const list = await apiGetJson<any[]>(page, '/api/projects').catch(() => []);
      const row = (list || []).find((p: any) => p.path === otherDir);
      if (row) await csrfFetch(page, `/api/projects/${row.id}`, { method: 'DELETE' });
      if (existsSync(join(ownDir!, ownFile))) rmSync(join(ownDir!, ownFile), { force: true });
      rmSync(otherDir, { recursive: true, force: true });
    }
  });

  // A task's explorer is its own screen: its worktree is another directory, and a person reaches one task and
  // then the next. One record shared by a project and all of its tasks answers those screens with each other's
  // files, and answers the task screens with no scroll position at all.
  test('each task explorer keeps its own files, and the project explorer keeps its own', async ({ page }) => {
    test.setTimeout(90000);
    const projectId = await getProjectId(page);
    const projectDir = ownProjectPath();
    expect(projectDir, 'this worker has no own project path').toBeTruthy();
    const config = await apiGetJson<{ workspace_dir: string }>(page, '/api/config');
    const workspaceDir = config?.workspace_dir || '';

    const stamp = Date.now();
    // Committed, because a task's worktree is a branch off the project's commit: a file the checkout never
    // committed is listed by the project explorer and by no worktree.
    const alpha = `tabs-alpha-${stamp}.txt`;
    const beta = `tabs-beta-${stamp}.txt`;
    const projectOnly = `tabs-project-only-${stamp}.txt`;
    writeFileSync(join(projectDir!, alpha), Array.from({ length: 400 }, (_, i) => `line ${i + 1}`).join('\n') + '\n');
    writeFileSync(join(projectDir!, beta), 'Beta content\n');
    writeFileSync(join(projectDir!, projectOnly), 'Only the project holds this\n');
    gitIn(projectDir!, `add "${alpha}" "${beta}"`);
    gitIn(projectDir!, '-c user.email="e2e@mike.local" -c user.name="e2e" commit -qm "seed explorer tabs"');

    // An explicit title and a one-word prompt keep these tasks out of the executor: the spec needs two
    // worktrees, not two turns.
    const makeTask = async (title: string): Promise<string> => {
      const created = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
        method: 'POST',
        body: { title, prompt: 'x', status: 'backlog' },
      });
      expect(created.ok, `create task: ${JSON.stringify(created.data)}`).toBe(true);
      // The worktree is made by the screen that opens it, while the listing reads the directory for itself:
      // a task that has never run can be listed while its checkout is still being made. This is the screen's
      // own idempotent init call, made before the screen opens, and the file is waited for where it lands.
      const inited = await csrfFetch(page, `/api/projects/${projectId}/tasks/${created.data.id}/init`, { method: 'POST' });
      expect(inited.ok, `init task: ${JSON.stringify(inited.data)}`).toBe(true);
      await expect.poll(() => existsSync(join(workspaceDir, projectId, created.data.id, alpha)), { timeout: 20000 }).toBe(true);
      return created.data.id as string;
    };
    const alphaTitle = `Explorer tabs alpha ${stamp}`;
    const betaTitle = `Explorer tabs beta ${stamp}`;
    const alphaTask = await makeTask(alphaTitle);
    await makeTask(betaTitle);

    const openExplorerOf = async (title: string) => {
      await page.locator('.card', { hasText: title }).first().click();
      await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 20000 });
      await page.locator('.task-detail-page [aria-label="Open explorer"]').click();
      await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 20000 });
      await expect(page.locator('.file-browser-list')).toBeVisible({ timeout: 20000 });
    };
    const closeExplorer = async () => {
      await page.locator('.explorer-header .close-btn').click();
      await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 20000 });
    };
    const openPinned = async (name: string) => {
      await page.locator('.file-browser-item', { hasText: name }).dblclick();
      await expect(page.locator('.editor-tab-name').filter({ hasText: name })).toBeVisible({ timeout: 20000 });
    };
    const editorScroll = () => page.locator('.cm-scroller').first().evaluate((el) => el.scrollTop);

    try {
      await gotoProject(page, `/?project=${projectId}`);
      await expect(page.locator('.card', { hasText: alphaTitle })).toBeVisible({ timeout: 20000 });

      // Task one: one file, read partway down. A tab write waits out a quiet period before IndexedDB sees it,
      // so a close or a navigation that should persist the last click waits that window out.
      await openExplorerOf(alphaTitle);
      await openPinned(alpha);
      await page.locator('.cm-scroller').first().evaluate((el) => { el.scrollTop = 900; });
      await expect.poll(editorScroll).toBeGreaterThan(0);
      await page.waitForTimeout(400);
      await closeExplorer();

      // The next task is a different screen, reached without the app reloading: it has never had a file
      // open, and it is not handed the file the first one left open.
      await openExplorerOf(betaTitle);
      await expect(page.locator('.editor-tab')).toHaveCount(0);
      await openPinned(beta);
      await closeExplorer();

      // Back to the first task: its own file, where it was left.
      await gotoProject(page, `/?project=${projectId}`);
      await openExplorerOf(alphaTitle);
      await expect(page.locator('.editor-tab-name').filter({ hasText: alpha })).toBeVisible({ timeout: 20000 });
      await expect(page.locator('.editor-tab-name').filter({ hasText: beta })).toHaveCount(0);
      await expect.poll(editorScroll, { timeout: 15000 }).toBeGreaterThan(0);
      await closeExplorer();

      // The project's explorer is a third screen: no task's file reaches it, and what it opens stays in it.
      await gotoProject(page, `/?project=${projectId}&view=explorer`);
      await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 20000 });
      await expect(page.locator('.editor-tab')).toHaveCount(0);
      await openPinned(projectOnly);
      await page.waitForTimeout(400);   // the write window

      await gotoProject(page, `/?project=${projectId}&task=${alphaTask}&view=task-explorer`);
      await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 20000 });
      await expect(page.locator('.editor-tab-name').filter({ hasText: alpha })).toBeVisible({ timeout: 20000 });
      await expect(page.locator('.editor-tab-name').filter({ hasText: projectOnly })).toHaveCount(0);

      await gotoProject(page, `/?project=${projectId}&view=explorer`);
      await expect(page.locator('.editor-tab-name').filter({ hasText: projectOnly })).toBeVisible({ timeout: 20000 });
      await expect(page.locator('.editor-tab-name').filter({ hasText: alpha })).toHaveCount(0);
    } finally {
      for (const name of [alpha, beta, projectOnly]) {
        try { rmSync(join(projectDir!, name), { force: true }); } catch { /* ignore */ }
      }
    }
  });

  // A task's explorer record is temporary, and the board is what says so: the move into Done is made by a card,
  // by the commit dialog, by another browser and by the executor, so the sweep reads the board.
  test('a task that moves to Done loses the files its explorer left open', async ({ page }) => {
    test.setTimeout(90000);
    const projectId = await getProjectId(page);
    const projectDir = ownProjectPath();
    expect(projectDir, 'this worker has no own project path').toBeTruthy();
    const config = await apiGetJson<{ workspace_dir: string }>(page, '/api/config');
    const workspaceDir = config?.workspace_dir || '';

    const stamp = Date.now();
    const title = `Explorer done ${stamp}`;
    const file = `tabs-done-${stamp}.txt`;
    writeFileSync(join(projectDir!, file), 'Committed, so the worktree holds it\n');
    gitIn(projectDir!, `add "${file}"`);
    gitIn(projectDir!, '-c user.email="e2e@mike.local" -c user.name="e2e" commit -qm "seed the done sweep"');
    const created = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
      method: 'POST',
      body: { title, prompt: 'x', status: 'backlog' },
    });
    expect(created.ok, `create task: ${JSON.stringify(created.data)}`).toBe(true);
    const taskId = created.data.id as string;
    const inited = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/init`, { method: 'POST' });
    expect(inited.ok, `init task: ${JSON.stringify(inited.data)}`).toBe(true);
    await expect.poll(() => existsSync(join(workspaceDir, projectId, taskId, file)), { timeout: 20000 }).toBe(true);

    try {
      await gotoProject(page, `/?project=${projectId}`);
      await page.locator('.card', { hasText: title }).first().click();
      await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 20000 });
      await page.locator('.task-detail-page [aria-label="Open explorer"]').click();
      await expect(page.locator('.file-browser-list')).toBeVisible({ timeout: 30000 });
      await page.locator('.file-browser-item', { hasText: file }).dblclick();
      await expect(page.locator('.editor-tab-name').filter({ hasText: file })).toBeVisible({ timeout: 20000 });
      await page.waitForTimeout(400);   // the write window
      await page.locator('.explorer-header .close-btn').click();
      await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 20000 });

      // The sweep judges a board, so a board is what has to be on screen when the move arrives from outside
      // this tab: the reading before it holds the task in Backlog, so the frame that moves it says a row changed.
      await gotoProject(page, `/?project=${projectId}`);
      await expect(page.locator('.card', { hasText: title })).toBeVisible({ timeout: 20000 });

      for (const status of ['review', 'done']) {
        const moved = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
          method: 'POST',
          body: { status },
        });
        expect(moved.ok, `move to ${status}: ${JSON.stringify(moved.data)}`).toBe(true);
      }
      await expect(page.locator('[data-column-id="done"] .card', { hasText: title })).toBeVisible({ timeout: 20000 });

      // The record alone was holding those tabs: the file is still on disk, and this screen has never seen one.
      await gotoProject(page, `/?project=${projectId}&task=${taskId}&view=task-explorer`);
      await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 30000 });
      await expect(page.locator('.editor-tab')).toHaveCount(0);
    } finally {
      try { rmSync(join(projectDir!, file), { force: true }); } catch { /* ignore */ }
    }
  });
});
