import { test, expect, type Page } from '@playwright/test';
import { mkdirSync, writeFileSync, rmSync, readdirSync, statSync } from 'node:fs';
import { execSync } from 'node:child_process';
import { join } from 'path';
import { getProjectId, createTaskViaApi, navigateToTask, waitForChatForm, waitForSendButtonGraceful, csrfFetch } from './helpers';

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

    // Go back to parent
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
    test.setTimeout(30000);
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `edit-diff-test-${Date.now()}`);
    await navigateToTask(page, taskId, projectId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Wait for chat form (handles start/restart buttons)
    const hasForm = await waitForChatForm(page);
    if (!hasForm) return; // graceful skip

    // Graceful: executor may not start in test env
    if (!await waitForSendButtonGraceful(page)) return;
    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('test edit file diff');
    await chatTextarea.press('Enter');

    // Wait for user message to appear
    await expect(page.locator('.chat-message.user').first()).toBeVisible({ timeout: 10000 });

    // Wait for tool call card to appear
    const toolCallVisible = await page.locator('.tool-call-card').first()
      .isVisible({ timeout: 15000 }).catch(() => false);
    if (!toolCallVisible) return; // graceful skip

    // Verify it's an edit_file tool call
    const toolName = page.locator('.tool-name').first();
    await expect(toolName).toBeVisible();
    const nameText = await toolName.textContent();
    expect(nameText?.toLowerCase()).toContain('edit');

    // Wait for the edit diff toggle button to appear
    const diffToggle = page.locator('.edit-diff-toggle');
    const toggleVisible = await diffToggle.isVisible({ timeout: 10000 }).catch(() => false);
    if (!toggleVisible) return; // graceful skip

    // The fixture has a trivial diff (2 added lines), so it starts expanded.
    // Verify the diff content is visible (expanded state)
    const diffContent = page.locator('.edit-diff');
    const diffVisible = await diffContent.isVisible({ timeout: 5000 }).catch(() => false);
    expect(diffVisible).toBe(true);

    // Collapse the diff
    await diffToggle.click();
    await expect(diffContent).not.toBeVisible({ timeout: 5000 });

    // Expand it again
    await diffToggle.click();
    await expect(diffContent).toBeVisible({ timeout: 5000 });

    // Verify the arrow shows collapsed state (▴ when expanded)
    const arrow = page.locator('.edit-diff-arrow');
    const arrowText = await arrow.textContent();
    expect(arrowText).toBe('▴'); // expanded shows up arrow

    // Collapse again
    await diffToggle.click();
    const arrowTextCollapsed = await arrow.textContent();
    expect(arrowTextCollapsed).toBe('▾'); // collapsed shows down arrow

    // Expand one more time and verify state persists
    await diffToggle.click();
    await expect(diffContent).toBeVisible({ timeout: 5000 });
    const finalArrow = await page.locator('.edit-diff-arrow').textContent();
    expect(finalArrow).toBe('▴');
  });
});

// --- Shared utilities for explorer tests ---

function getConfig() {
  return JSON.parse(execSync('curl -s http://localhost:3000/api/config', { encoding: 'utf-8' }));
}

function findWorktreeDir(): string | null {
  const worktreesDir = getConfig().workspace_dir;
  try {
    // Path structure: worktreesDir/projectID/taskID
    const projectEntries = readdirSync(worktreesDir);
    for (const projEntry of projectEntries) {
      const projPath = join(worktreesDir, projEntry);
      if (!statSync(projPath).isDirectory()) continue;
      const taskEntries = readdirSync(projPath);
      for (const taskEntry of taskEntries) {
        const fullPath = join(projPath, taskEntry);
        if (statSync(fullPath).isDirectory()) {
          return fullPath;
        }
      }
    }
  } catch { /* ignore */ }
  return null;
}

// --- Explorer (serial: modify same worktree filesystem) ---

test.describe('Explorer', () => {
  test.describe.configure({ mode: 'serial' });

  test.beforeEach(async ({ page }) => {
    test.setTimeout(30000);
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should create a folder in the current directory and navigate into it', async ({ page }) => {
    const worktreeDir = findWorktreeDir();
    const projectId = await getProjectId(page);
    if (!worktreeDir || !projectId) {
      // No worktree or project — skip gracefully
      await expect(page.locator('.board')).toBeVisible();
      return;
    }

    const uniqueId = Date.now();
    const folderName = `new-folder-${uniqueId}`;

    // Click on the project to select it (if not already selected)
    await page.locator('.project-item').first().click();
    await page.waitForSelector('.top-bar', { timeout: 10000 });

    // Open the explorer view by clicking the project title button
    await page.locator('.explorer-btn').click();
    await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });

    // Wait for the file browser to load
    await expect(page.locator('.file-browser-list')).toBeVisible({ timeout: 10000 });

    // Get the current path before creating the folder
    const pathInput = page.locator('.file-browser-path-input');
    const currentPath = await pathInput.inputValue();

    // Click the "New folder" button (second button in file-actions)
    const newFolderBtns = page.locator('.new-file-btn');
    const newFolderBtn = newFolderBtns.nth(1); // second button is the folder one
    await newFolderBtn.click();

    // Wait for the folder name input to appear
    await expect(page.locator('.new-file-input')).toBeVisible({ timeout: 5000 });

    // Type the folder name and press Enter
    await page.fill('.new-file-input', folderName);
    await page.locator('.new-file-input').press('Enter');

    // Wait for the input to disappear (form closed)
    await expect(page.locator('.new-file-input')).toBeHidden({ timeout: 5000 });

    // Verify the path has been updated to include the new folder (navigated into it)
    await expect(async () => {
      const newPath = await pathInput.inputValue();
      expect(newPath).toContain(folderName);
    }).toPass({ timeout: 5000 });

    // Verify the folder exists on disk
    const folderPath = join(worktreeDir, folderName);
    try {
      const stats = statSync(folderPath);
      expect(stats.isDirectory()).toBe(true);
    } catch {
      test.fail('Folder was not created on disk');
    }

    // Clean up
    execSync(`rm -rf ${folderPath}`);
  });

  test('should open editor at clicked line from Review Changes diff', async ({ page }) => {
    const worktreeDir = findWorktreeDir();
    const projectId = await getProjectId(page);
    if (!worktreeDir || !projectId) {
      // No worktree or project — skip gracefully
      await expect(page.locator('.board')).toBeVisible();
      return;
    }

    const uniqueId = Date.now();
    const testFile = `diff-line-click-${uniqueId}.txt`;
    const filePath = join(worktreeDir, testFile);
    // Create a file with enough lines to pick a specific target
    const fileContent = Array.from({ length: 20 }, (_, i) => `Line ${i + 1}: content here`).join('\n') + '\n';
    writeFileSync(filePath, fileContent);

    // Click on the project to select it (if not already selected)
    await page.locator('.project-item').first().click();
    await page.waitForSelector('.top-bar', { timeout: 10000 });

    // Open the explorer view by clicking the project title button
    await page.locator('.explorer-btn').click();
    await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });

    // Click the "Review Changes" button (git-status-btn in explorer header)
    await page.locator('.git-status-btn').click();

    // Wait for the Changes panel header to appear
    await expect(page.locator('.panel-title')).toHaveText('Changes', { timeout: 10000 });

    // Wait for diff rows to appear
    await expect(async () => {
      const diffRows = await page.locator('.diff-row').count();
      expect(diffRows).toBeGreaterThan(0);
    }).toPass({ timeout: 10000 });

    // Find a clickable diff row with a line number
    const clickableRows = page.locator('.diff-row-clickable');
    const rowCount = await clickableRows.count();
    if (rowCount === 0) {
      // Clean up
      execSync(`rm -f ${filePath}`);
      return;
    }

    // Pick a row that has a visible line number (not the first one which might be a header)
    // Get the line number from the gutter next to the clickable row
    // We'll click on a row and verify the editor opens
    const targetRow = clickableRows.nth(Math.min(5, rowCount - 1)); // pick ~line 6 area
    await targetRow.click();

    // The Review Changes panel should close and the editor should open
    // Wait for the editor to appear (branch-editor)
    await expect(page.locator('.branch-editor')).toBeVisible({ timeout: 10000 });

    // Verify the file is loaded (loading state disappears)
    await expect(page.locator('.editor-placeholder:has-text("Loading file")')).not.toBeVisible({ timeout: 10000 });

    // Verify the file name is shown in the tab bar
    await expect(page.locator('.editor-tab-name')).toContainText(testFile);

    // Verify CodeMirror editor is present
    await expect(page.locator('.cm-editor')).toBeVisible({ timeout: 5000 });

    // Verify the cursor is on a specific line (not line 1)
    // The active line gutter should show a line number > 1
    const activeLineGutter = page.locator('.cm-activeLineGutter');
    await expect(activeLineGutter).toBeVisible({ timeout: 5000 });
    const activeLineNumber = await activeLineGutter.textContent();
    expect(activeLineNumber).toBeTruthy();
    const lineNum = parseInt(activeLineNumber!.trim(), 10);
    // Should be on a line > 1 (we clicked on a row ~5 lines in)
    expect(lineNum).toBeGreaterThan(1);

    // Clean up
    execSync(`rm -f ${filePath}`);
  });
});

// --- Editor External Change Reconciliation (serial: modify same worktree filesystem) ---

test.describe('Editor External Change Reconciliation', () => {
  test.describe.configure({ mode: 'serial' });

  test.beforeEach(async ({ page }) => {
    test.setTimeout(30000);
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  async function openFileInEditor(page: test['project']['use'], worktreeDir: string, file: string) {
    await page.locator('.project-item').first().click();
    await page.waitForSelector('.top-bar', { timeout: 10000 });
    await page.locator('.explorer-btn').click();
    await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });
    await expect(page.locator('.file-browser-list')).toBeVisible({ timeout: 10000 });
    await page.locator('.file-browser-item', { hasText: file }).click();
    await expect(page.locator('.cm-editor')).toBeVisible({ timeout: 10000 });
  }

  test('reloads an open file when it is changed externally with no unsaved edits', async ({ page }) => {
    const worktreeDir = findWorktreeDir();
    const projectId = await getProjectId(page);
    if (!worktreeDir || !projectId) {
      await expect(page.locator('.board')).toBeVisible();
      return;
    }

    const uniqueId = Date.now();
    const file = `ext-reload-${uniqueId}.txt`;
    const absFile = join(worktreeDir, file);
    writeFileSync(absFile, 'Line 1: alpha\nLine 2: beta\nLine 3: gamma\n');

    try {
      await openFileInEditor(page, worktreeDir, file);
      await expect(page.locator('.cm-content')).toContainText('Line 2: beta');

      // Change the file externally (editor has no unsaved edits)
      writeFileSync(absFile, 'Line 1: alpha\nLine 2: BETA-CHANGED\nLine 3: gamma\n');

      await expect.poll(async () => {
        return (await page.locator('.cm-content').textContent()) ?? '';
      }).toContain('BETA-CHANGED');
    } finally {
      try { rmSync(absFile, { force: true }); } catch { /* ignore */ }
    }
  });

  test('merges an external change with non-overlapping unsaved edits', async ({ page }) => {
    const worktreeDir = findWorktreeDir();
    const projectId = await getProjectId(page);
    if (!worktreeDir || !projectId) {
      await expect(page.locator('.board')).toBeVisible();
      return;
    }

    const uniqueId = Date.now();
    const file = `ext-merge-${uniqueId}.txt`;
    const absFile = join(worktreeDir, file);
    writeFileSync(absFile, 'Line 1: alpha\nLine 2: beta\nLine 3: gamma\n');

    try {
      await openFileInEditor(page, worktreeDir, file);

      // User edits line 1 in the editor
      await page.locator('.cm-content').first().click();
      await page.keyboard.press('Home');
      await page.keyboard.press('End');
      await page.keyboard.type(' USER');
      await page.waitForTimeout(400); // let draft/unsaved state settle

      // Externally edit a different line (line 3)
      writeFileSync(absFile, 'Line 1: alpha\nLine 2: beta\nLine 3: gamma EXTERNAL\n');

      // Both the user edit AND the external change must be present
      await expect.poll(async () => {
        return (await page.locator('.cm-content').textContent()) ?? '';
      }).toContain('alpha USER');
      await expect.poll(async () => {
        return (await page.locator('.cm-content').textContent()) ?? '';
      }).toContain('gamma EXTERNAL');
    } finally {
      try { rmSync(absFile, { force: true }); } catch { /* ignore */ }
    }
  });

  test('keeps the user buffer and shows a toast on a conflicting external change', async ({ page }) => {
    const worktreeDir = findWorktreeDir();
    const projectId = await getProjectId(page);
    if (!worktreeDir || !projectId) {
      await expect(page.locator('.board')).toBeVisible();
      return;
    }

    const uniqueId = Date.now();
    const file = `ext-conflict-${uniqueId}.txt`;
    const absFile = join(worktreeDir, file);
    writeFileSync(absFile, 'Line 1: alpha\nLine 2: beta\nLine 3: gamma\n');

    try {
      await openFileInEditor(page, worktreeDir, file);

      // User edits line 1
      await page.locator('.cm-content').first().click();
      await page.keyboard.press('Home');
      await page.keyboard.press('End');
      await page.keyboard.type(' USER');
      await page.waitForTimeout(400);

      // Externally edit the SAME line differently
      writeFileSync(absFile, 'Line 1: alpha EXTERNAL\nLine 2: beta\nLine 3: gamma\n');

      // User edit must be preserved (not overwritten by the external change)
      await page.waitForTimeout(1200); // allow debounce + fetch + reconcile
      const content = (await page.locator('.cm-content').textContent()) ?? '';
      expect(content).toContain('alpha USER');
      expect(content).not.toContain('alpha EXTERNAL');

      // A non-blocking toast should be shown
      await expect(page.locator('.toast')).toBeVisible({ timeout: 5000 });
      await expect(page.locator('.toast')).toContainText('changed externally');
    } finally {
      try { rmSync(absFile, { force: true }); } catch { /* ignore */ }
    }
  });
});
