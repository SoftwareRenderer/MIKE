import { test, expect, type Page } from '@playwright/test';
import { mkdirSync, writeFileSync, rmSync, readdirSync, statSync } from 'node:fs';
import { execSync } from 'node:child_process';
import { join } from 'path';
import { createTaskViaApi, csrfFetch, getProjectId, navigateToTask, projectDirPath, selectProject, waitForChatForm, waitForSendButton, waitForSendButtonGraceful, waitForWorktree, writeFileInWorktree } from './helpers';


test.describe.configure({ mode: 'parallel' });

// --- File Browser Actions (serial: share test directories, modify projects) ---

const TEST_NON_GIT = '/tmp/kanban-test-nongit';
const TEST_NEW_FOLDER_PARENT = '/tmp/kanban-test-mkdir-parent';

test.describe('File Browser Actions', () => {
  test.describe.configure({ mode: 'serial' });

  /** Wait for the project created from `path` to show up in the sidebar.
   *  Identity-based on purpose: the chromium project runs 4 workers against one
   *  server, so a global `.project-item` count moves under this test whenever
   *  another spec adds or removes a project. */
  async function expectProjectVisible(page: Page, path: string): Promise<void> {
    const name = path.split('/').pop() as string;
    await expect(page.locator('.project-item', { hasText: name }).first()).toBeVisible({ timeout: 10000 });
  }

  /** DELETE the project recorded for `path`, if any (idempotent pre-clean). */
  async function removeProjectByPath(page: Page, path: string): Promise<void> {
    const list = await page.evaluate(async () => await (await fetch('/api/projects')).json());
    if (!Array.isArray(list)) return;
    const stale = list.find((p: any) => p.path === path);
    if (stale) await csrfFetch(page, `/api/projects/${stale.id}`, { method: 'DELETE' });
  }

  test.beforeEach(async ({ page }) => {
    try { rmSync(TEST_NON_GIT, { recursive: true, force: true }); } catch { /* ignore */ }
    mkdirSync(TEST_NON_GIT, { recursive: true });
    writeFileSync(`${TEST_NON_GIT}/README.txt`, 'not a git repo');

    try { rmSync(TEST_NEW_FOLDER_PARENT, { recursive: true, force: true }); } catch { /* ignore */ }
    mkdirSync(TEST_NEW_FOLDER_PARENT, { recursive: true });
    execSync('git init', { cwd: TEST_NEW_FOLDER_PARENT });
    execSync('git -c user.email="test@kanban.local" -c user.name="Test" commit --allow-empty -m "init"', { cwd: TEST_NEW_FOLDER_PARENT });

    await page.goto('/');
    await page.waitForSelector('.project-item, .empty-state', { timeout: 10000 });
  });

  test('should initialize git in a non-git directory and select it as project', async ({ page }) => {
    // This describe re-creates the directory from scratch, so drop any project an
    // earlier run left pointing at the same path (it would collide with the
    // create below instead of exercising it).
    await removeProjectByPath(page, TEST_NON_GIT);

    // Open the file browser (uses BaseModal with role="dialog")
    await page.click('.add-project-btn');
    await expect(page.locator('[role="dialog"]:has-text("Add Project")')).toBeVisible({ timeout: 5000 });

    // Navigate to the non-git directory
    const pathInput = page.locator('.path-input');
    await pathInput.fill(TEST_NON_GIT);
    await pathInput.press('Enter');

    // Wait for current path to update (confirms navigation completed)
    await expect(async () => {
      const pathText = await page.locator('.current-path').textContent();
      expect(pathText).toContain('kanban-test-nongit');
    }).toPass({ timeout: 5000 });

    // Git indicator should not be visible (only shown for git repos)
    await expect(page.locator('.git-indicator')).not.toBeVisible();

    // Footer button should say "Initialize Git"
    await expect(page.locator('.btn-primary')).toContainText('Initialize Git');

    // Click "Initialize Git"
    await page.click('.btn-primary');

    // Wait for init to complete by polling the API (via page.evaluate for auth)
    await expect(async () => {
      const data = await page.evaluate(async (path) => {
        const res = await fetch(`/api/fs?path=${encodeURIComponent(path)}`);
        return res.json();
      }, TEST_NON_GIT);
      expect(data.isGitRepo).toBe(true);
    }).toPass({ timeout: 15000 });

    // Browser should close automatically after init (auto-select)
    await expect(page.locator('[role="dialog"]:has-text("Add Project")')).not.toBeVisible({ timeout: 5000 });

    // The new project is listed
    await expectProjectVisible(page, TEST_NON_GIT);

    // Board should be visible (project was selected)
    await expect(page.locator('.board')).toBeVisible({ timeout: 5000 });
  });

  test('should show error when creating a folder that already exists', async ({ page }) => {
    await page.click('.add-project-btn');
    await expect(page.locator('[role="dialog"]:has-text("Add Project")')).toBeVisible({ timeout: 5000 });

    const pathInput = page.locator('.path-input');
    await pathInput.fill(TEST_NEW_FOLDER_PARENT);
    await pathInput.press('Enter');
    await expect(page.locator('.git-indicator')).toBeVisible({ timeout: 5000 });

    // Open new folder dialog
    await page.click('.icon-btn-sm[title="New folder"]');
    await expect(page.locator('.folder-name-input')).toBeVisible({ timeout: 5000 });

    // Create a folder
    const folderName = `dup-folder-${Date.now()}`;
    await page.fill('.folder-name-input', folderName);
    await page.locator('.folder-name-input').press('Enter');
    await expect(page.locator('.folder-name-input')).toBeHidden({ timeout: 5000 });

    await pathInput.fill(TEST_NEW_FOLDER_PARENT);
    await pathInput.press('Enter');
    await expect(page.locator('.file-list')).toBeVisible({ timeout: 5000 });

    // Try to create the same folder again
    await page.click('.icon-btn-sm[title="New folder"]');
    await expect(page.locator('.folder-name-input')).toBeVisible({ timeout: 5000 });
    await page.fill('.folder-name-input', folderName);
    await page.locator('.folder-name-input').press('Enter');

    // Should show an error
    await expect(page.locator('.error')).toBeVisible({ timeout: 5000 });
    const errorText = await page.locator('.error').textContent();
    expect(errorText).toContain('already exists');
  });

  test('should create project with sandbox disabled when checkbox is unchecked', async ({ page }) => {

    // Open the file browser (uses BaseModal with role="dialog")
    await page.click('.add-project-btn');
    await expect(page.locator('[role="dialog"]:has-text("Add Project")')).toBeVisible({ timeout: 5000 });

    // Uncheck "Use Sandbox"
    const sandboxCheckbox = page.locator('.sandbox-toggle input[type="checkbox"]');
    await sandboxCheckbox.click();
    await expect(sandboxCheckbox).not.toBeChecked();

    // Navigate to a unique directory
    const uniqueDir = `/tmp/kanban-test-sandbox-off-${Date.now()}`;
    try { rmSync(uniqueDir, { recursive: true, force: true }); } catch { /* ignore */ }
    mkdirSync(uniqueDir, { recursive: true });
    execSync('git init', { cwd: uniqueDir });
    execSync('git -c user.email="test@kanban.local" -c user.name="Test" commit --allow-empty -m "init"', { cwd: uniqueDir });

    const pathInput = page.locator('.path-input');
    await pathInput.fill(uniqueDir);
    await pathInput.press('Enter');

    // Wait for git indicator
    await expect(page.locator('.git-indicator')).toBeVisible({ timeout: 5000 });

    // Click Select (uses .btn-primary class)
    await page.click('.btn-primary:has-text("Select")');

    // Browser should close
    await expect(page.locator('[role="dialog"]:has-text("Add Project")')).not.toBeVisible({ timeout: 5000 });

    // The new project is listed
    await expectProjectVisible(page, uniqueDir);

    // Verify via API that the last project has sandbox_enabled: false
    await expect(async () => {
      const projects = await page.evaluate(async () => {
        const res = await fetch('/api/projects');
        return res.json();
      });
      const lastProject = projects[projects.length - 1];
      expect(lastProject.sandbox_enabled).toBe(false);
    }).toPass({ timeout: 5000 });
  });
});

// --- Helper: type @ and trigger input event manually (Playwright type() doesn't always fire Svelte oninput) ---
async function typeAtSign(page: test['project']['use'], textarea: any) {
  await textarea.click();
  await page.evaluate(el => {
    (el as HTMLTextAreaElement).value = '@';
    (el as HTMLTextAreaElement).setSelectionRange(1, 1);
    el.dispatchEvent(new Event('input', { bubbles: true }));
  }, await textarea.elementHandle());
}

// --- File @ Mention (parallel: each test is independent) ---

test.describe('File @ Mention', () => {
  test.beforeEach(async ({ page }) => {
    test.setTimeout(120000);
    await page.goto('/');
    await page.waitForSelector('.card');

    // Mock the fetch to return file results for the @ mention search
    await page.route('/api/fs/search*', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          root: '/tmp/test-repo',
          results: [
            { path: 'README.md' },
            { path: 'src/app.ts' },
            { path: 'src/components/Button.svelte' },
            { path: 'src/utils.ts' },
            { path: 'package.json' },
          ],
        }),
      });
    });
  });

  test('should show file mention dropdown when typing @ in create task modal', async ({ page }) => {
    // Open the create task modal
    await page.locator('.add-task-btn').click();
    await expect(page.locator('#task-description')).toBeVisible();

    // Type @ and trigger input
    const textarea = page.locator('#task-description');
    await typeAtSign(page, textarea);

    // Wait for the dropdown to appear
    await expect(page.locator('.mention-dropdown')).toBeVisible({ timeout: 5000 });

    // Should show file results
    const items = page.locator('.mention-item');
    await expect(items).toHaveCount(5);
  });

  test('should navigate dropdown with arrow keys', async ({ page }) => {
    await page.locator('.add-task-btn').click();
    await expect(page.locator('#task-description')).toBeVisible();

    const textarea = page.locator('#task-description');
    await typeAtSign(page, textarea);
    await expect(page.locator('.mention-dropdown')).toBeVisible({ timeout: 5000 });

    const items = page.locator('.mention-item');

    // First item should be active by default
    await expect(items.first()).toHaveClass(/active/);

    // Press ArrowDown to highlight second item
    await textarea.press('ArrowDown');
    await expect(items.nth(1)).toHaveClass(/active/);

    // Press ArrowUp to go back to first
    await textarea.press('ArrowUp');
    await expect(items.first()).toHaveClass(/active/);
  });

  test('should select with Enter key', async ({ page }) => {
    await page.locator('.add-task-btn').click();
    await expect(page.locator('#task-description')).toBeVisible();

    const textarea = page.locator('#task-description');
    await typeAtSign(page, textarea);
    await expect(page.locator('.mention-dropdown')).toBeVisible({ timeout: 5000 });

    // Press ArrowDown to select second item
    await textarea.press('ArrowDown');
    await expect(page.locator('.mention-item').nth(1)).toHaveClass(/active/);

    // Press Enter to select
    await textarea.press('Enter');

    // Dropdown should disappear
    await expect(page.locator('.mention-dropdown')).not.toBeVisible();

    // Textarea should contain @path of second item
    const value = await textarea.inputValue();
    expect(value).toBe('@src/app.ts');
  });

  test('should close dropdown with Escape', async ({ page }) => {
    await page.locator('.add-task-btn').click();
    await expect(page.locator('#task-description')).toBeVisible();

    const textarea = page.locator('#task-description');
    await typeAtSign(page, textarea);
    await expect(page.locator('.mention-dropdown')).toBeVisible({ timeout: 5000 });

    // Press Escape to close
    await textarea.press('Escape');

    // Dropdown should be hidden
    await expect(page.locator('.mention-dropdown')).not.toBeVisible();

    // The @ should still be in the textarea
    const value = await textarea.inputValue();
    expect(value).toBe('@');
  });

  test('should select a file by clicking without closing the modal', async ({ page }) => {
    await page.locator('.add-task-btn').click();
    await expect(page.locator('#task-description')).toBeVisible();

    const textarea = page.locator('#task-description');
    await typeAtSign(page, textarea);
    await expect(page.locator('.mention-dropdown')).toBeVisible({ timeout: 5000 });

    // Click the first result
    await page.locator('.mention-item').first().click();

    // Dropdown should disappear
    await expect(page.locator('.mention-dropdown')).not.toBeVisible();

    // Modal should STILL be open (not closed by the click)
    await expect(page.locator('#task-description')).toBeVisible();

    // Textarea should contain @path of first item
    const value = await textarea.inputValue();
    expect(value).toBe('@README.md');
  });

  test('should filter results by query after @', async ({ page }) => {
    // Update mock to filter based on query
    await page.route('/api/fs/search*', async (route) => {
      const url = new URL(route.request().url());
      const q = url.searchParams.get('q') || '';

      const allFiles = [
        { path: 'README.md' },
        { path: 'src/app.ts' },
        { path: 'src/components/Button.svelte' },
        { path: 'src/utils.ts' },
        { path: 'package.json' },
      ];

      const results = q
        ? allFiles.filter(f => f.path.toLowerCase().includes(q.toLowerCase()))
        : allFiles;

      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ root: '/tmp/test-repo', results }),
      });
    });

    await page.locator('.add-task-btn').click();
    await expect(page.locator('#task-description')).toBeVisible();

    const textarea = page.locator('#task-description');
    await typeAtSign(page, textarea);

    // Wait for initial dropdown
    await expect(page.locator('.mention-dropdown')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.mention-item')).toHaveCount(5);

    // Type more after @ to filter
    await page.evaluate(el => {
      (el as HTMLTextAreaElement).value = '@src';
      (el as HTMLTextAreaElement).setSelectionRange(4, 4);
      el.dispatchEvent(new Event('input', { bubbles: true }));
    }, await textarea.elementHandle());

    // Wait for filtered results
    await expect(async () => {
      const count = await page.locator('.mention-item').count();
      expect(count).toBeGreaterThan(0);
      expect(count).toBeLessThan(5);
    }).toPass({ timeout: 5000 });
  });
});

// --- Edit Diff Expand Persistence (parallel) ---

test.describe('Edit Diff Expand Persistence', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should persist edit diff expanded state across virtual list remount', async ({ page }) => {
    // An executor turn plus a remount.
    test.setTimeout(120_000);
    const projectId = await getProjectId(page);
    // Keyword-free prompt: "edit-diff-test" matches the mock's `\bedit\b` route, and the
    // initial turn then replays this fixture with the same tool-call id — the remount test's
    // "our card" would be the initial turn's, not the one it sent.
    const taskId = await createTaskViaApi(page, projectId, `seed-turn-${Date.now()}`);
    await navigateToTask(page, taskId, projectId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // waitForChatForm clicks "Start task" on a new task; the task then answers its own prompt, and
    // that turn must finish first — otherwise the first tool card is the initial turn's, not ours.
    await waitForChatForm(page);
    await waitForSendButton(page);

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await expect(chatTextarea).toBeVisible({ timeout: 10000 });
    // Seed the file the fixture edits — the e2e repo holds only a README, so an unseeded
    // edit_file fails on a missing file and no edit-diff card is rendered.
    await waitForWorktree(projectId, taskId);
    writeFileInWorktree(projectId, taskId, 'src/lib/utils/format.ts', 'func one\nfunc two\n');
    const cardsBefore = await page.locator('.tool-call-card').count();
    // Pinned, not keyword-routed: the plan-first skill is in every system prompt, so a prompt
    // that merely mentions "edit" is not guaranteed to reach the edit_file fixture at all.
    await chatTextarea.fill(`test edit file diff ${Date.now()} llm-fixture:tool_call_edit_file`);
    await chatTextarea.press('Enter');

    // OUR card — the one that appears after the send.
    await expect
        .poll(() => page.locator('.tool-call-card').count(), { timeout: 30000, message: 'a tool-call card appears for the sent message' })
        .toBeGreaterThan(cardsBefore);
    const card = page.locator('.tool-call-card').last();

    await expect(card.locator('.tool-name')).toBeVisible();
    expect((await card.locator('.tool-name').textContent())?.toLowerCase()).toContain('edit');

    const diffToggle = card.locator('.edit-diff-toggle');
    const diffContent = card.locator('.edit-diff');
    const arrow = card.locator('.edit-diff-arrow');

    // The fixture's diff is two added lines and ToolCall auto-expands a small diff, so EXPANDED
    // is the starting state and the first click collapses it.
    await expect(diffContent).toBeVisible({ timeout: 10000 });
    await expect(arrow).toHaveText('▴');

    await diffToggle.click();
    await expect(diffContent).not.toBeVisible({ timeout: 5000 });
    await expect(arrow).toHaveText('▾');

    await diffToggle.click();
    await expect(diffContent).toBeVisible({ timeout: 5000 });

    // The remount the test is named for: leaving the task and coming back rebuilds the
    // virtualised message list from scratch. Toggling a diff must not be state that only
    // survives as long as the row does, or scrolling a long chat collapses diffs on the user.
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    await navigateToTask(page, taskId, projectId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    const reopened = page.locator('.tool-call-card', { hasText: 'edit' }).last();
    await expect(reopened.locator('.edit-diff')).toBeVisible({ timeout: 10000 });
    await expect(reopened.locator('.edit-diff-arrow')).toHaveText('▴');
  });
});

// --- Shared utilities for explorer tests ---

// --- Explorer (serial: modify same worktree filesystem) ---

test.describe('Explorer', () => {
  test.describe.configure({ mode: 'serial' });

  test.beforeEach(async ({ page }) => {
    test.setTimeout(30000);
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should create a folder in the current directory and navigate into it', async ({ page }) => {
    const projectId = await getProjectId(page);
    // The project explorer and Review-Changes read the PROJECT directory, not a task
    // worktree — see projectDirPath() in helpers.ts.
    const projectDir = await projectDirPath(page, projectId);

    const uniqueId = Date.now();
    const folderName = `new-folder-${uniqueId}`;

    // Select the project under test. `.project-item` + `.first()` is the first row of
    // the sidebar, which once per-worker projects exist is some other test's project —
    // the folder then gets created somewhere else and this test reads its own dir.
    await selectProject(page, projectId);
    await page.waitForSelector('.top-bar', { timeout: 10000 });

    // Open the explorer view by clicking the project title button
    await page.locator('.explorer-btn').click();
    await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });

    // Wait for the file browser to load
    await expect(page.locator('.file-browser-list')).toBeVisible({ timeout: 10000 });

    // Get the current path before creating the folder
    const pathInput = page.locator('.file-browser-path-input');
    const currentPath = await pathInput.inputValue();

    // The folder button is identified by its accessible name. The old selector was
    // `.new-file-btn` nth(1) — that is the NEW FILE button (the bar is Find / New file /
    // New folder), and the name it typed went into `.new-file-input`, so no folder was
    // ever created and the assertions below were unreachable.
    await page.locator('.new-file-btn[aria-label="Create new folder"]').click();
    // The project explorer is BranchFileBrowser: BOTH its new-file and new-folder rows
    // use class `.new-file-input`, so the accessible name is what disambiguates them.
    const folderInput = page.locator('.new-file-input[aria-label="New folder name"]');
    await expect(folderInput).toBeVisible({ timeout: 5000 });
    await folderInput.fill(folderName);
    await folderInput.press('Enter');

    // Creating a folder navigates into it (FileBrowser's handleCreateFolder -> loadDir).
    await expect(async () => {
      expect(await pathInput.inputValue()).toContain(folderName);
    }).toPass({ timeout: 5000 });

    // The folder really exists, in the PROJECT directory the explorer was showing
    // (not in the path it navigated to afterwards).
    const folderPath = join(projectDir, folderName);
    expect(statSync(folderPath).isDirectory()).toBe(true);

    execSync(`rm -rf ${folderPath}`);
  });
});
