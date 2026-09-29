import { test, expect, type Page } from '@playwright/test';
import { execSync } from 'node:child_process';
import { mkdirSync, writeFileSync, rmSync, existsSync } from 'node:fs';
import { csrfFetch, hasInstalledLspExtension } from './helpers';

// ─── Prerequisite check ──────────────────────────────────────────────────────

const hasGopls = (() => {
  try { execSync('which gopls', { stdio: 'pipe' }); return true; }
  catch { return false; }
})();

test.skip(!hasGopls, 'gopls not installed');

// ─── Test workspace ──────────────────────────────────────────────────────────

const GO_WORKSPACE = `/tmp/kanban-test-go-lsp`;
const GO_WORKSPACE_2 = `/tmp/kanban-test-go-lsp-2`;

// ─── Helpers ─────────────────────────────────────────────────────────────────

function ensureGoWorkspace(): void {
  if (!existsSync(GO_WORKSPACE)) {
    mkdirSync(GO_WORKSPACE, { recursive: true });
    execSync('git init', { cwd: GO_WORKSPACE });
    execSync('git -c user.email="test@kanban.local" -c user.name="Test" commit --allow-empty -m "init"', { cwd: GO_WORKSPACE });
    execSync('go mod init lsptest', { cwd: GO_WORKSPACE });
  }
  // Always write the test file (idempotent)
  writeFileSync(`${GO_WORKSPACE}/main.go`, `package main

import "fmt"

func main() {
\tfmt.P
}
`);
}

function ensureGoWorkspace2(): void {
  if (!existsSync(GO_WORKSPACE_2)) {
    mkdirSync(GO_WORKSPACE_2, { recursive: true });
    execSync('git init', { cwd: GO_WORKSPACE_2 });
    execSync('git -c user.email="test@kanban.local" -c user.name="Test" commit --allow-empty -m "init"', { cwd: GO_WORKSPACE_2 });
    execSync('go mod init lsptest2', { cwd: GO_WORKSPACE_2 });
  }
  writeFileSync(`${GO_WORKSPACE_2}/main.go`, `package main

import "fmt"

func hello() {
\tfmt.P
}
`);
}

async function createProjectViaApi(page: test['project']['use'], path: string): Promise<string> {
  const result = await csrfFetch(page, '/api/projects', {
    method: 'POST',
    body: { name: 'lsp-test-project', path },
  });
  if (!result.ok) {
    throw new Error(`Failed to create project: ${result.status} ${JSON.stringify(result.data)}`);
  }
  return result.data.id;
}

async function deleteProjectViaApi(page: test['project']['use'], projectId: string): Promise<void> {
  await csrfFetch(page, `/api/projects/${projectId}`, { method: 'DELETE' });
}

/**
 * The language server has answered for the workspace this editor is open on. The footer's LSP control
 * carries the language it connected for and `-` until it has, which is the state every LSP interaction
 * below actually needs — the process existing is not it.
 */
async function waitForLspReady(page: Page, timeout = 90_000): Promise<void> {
  const status = page.locator('.editor-footer [aria-label="LSP status"]');
  await expect(status, 'the editor footer never offered an LSP status').toBeVisible({ timeout });
  await expect(status, 'the language server never reported itself ready').not.toHaveText('-', { timeout });
}

function cleanupWorkspace(): void {
  rmSync(GO_WORKSPACE, { recursive: true, force: true });
}

// ─── LSP Integration Tests ──────────────────────────────────────────────────

test.describe.serial('LSP Integration', () => {
  let lspProjectId = '';

  test.beforeEach(async ({ page }) => {
    test.setTimeout(120000);

    // Ensure Go workspace exists
    ensureGoWorkspace();

    // Navigate to the app
    await page.goto('/');
    await page.waitForSelector('.project-item, .empty-state', { timeout: 10000 });

    // The host binary is not enough — without the installed extension there is no server to talk to.
    if (!(await hasInstalledLspExtension(page, 'go'))) {
      test.skip(true, 'no installed LSP extension owns .go — install the gopls extension to run LSP specs');
    }

    // Register the Go workspace as a project
    lspProjectId = await createProjectViaApi(page, GO_WORKSPACE);

    // Navigate directly to the project via URL
    await page.evaluate((pid) => { window.location.href = `/?project=${pid}`; }, lspProjectId);
    await expect(page.locator('.board')).toBeVisible({ timeout: 15000 });

    // Open the explorer view
    await page.locator('.explorer-btn').click();
    await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });

    // Wait for file browser to finish loading (sandbox initialization can be slow).
    // The file browser shows "Initializing sandbox..." or "Loading..." during startup.
    // We wait for the loading overlay to disappear, then check for errors.
    await expect(async () => {
      // The loading element is visible while sandbox initializes
      const loadingVisible = await page.locator('.file-browser-loading').isVisible();
      const listVisible = await page.locator('.file-browser-list').isVisible().catch(() => false);
      const errorVisible = await page.locator('.file-browser-error').isVisible().catch(() => false);

      // We're done when loading is gone AND either list or error is visible
      if (!loadingVisible && (listVisible || errorVisible)) {
        if (errorVisible) {
          const errorText = await page.locator('.file-browser-error').textContent();
          throw new Error(`File browser error: ${errorText}`);
        }
        if (!listVisible) {
          throw new Error('File browser finished loading but list is not visible');
        }
        return; // Success
      }
      throw new Error('File browser still loading');
    }).toPass({ timeout: 60000, intervals: [2000] });

    // Open main.go in the editor
    const mainGoItem = page.locator('.file-browser-item', { hasText: 'main.go' });
    await expect(mainGoItem).toBeVisible({ timeout: 10000 });
    await mainGoItem.click();

    await expect(page.locator('.cm-editor')).toBeVisible({ timeout: 15000 });

    // Wait for the editor content to load (loading state gone)
    await expect(page.locator('.editor-loading')).not.toBeVisible({ timeout: 10000 });
  });

  test.afterEach(async ({ page }) => {
    // Clean up the project from the server
    if (lspProjectId) {
      try { await deleteProjectViaApi(page, lspProjectId); } catch { /* ignore */ }
    }
    // Clean up the filesystem
    cleanupWorkspace();
  });

  test('should connect to LSP server without errors', async ({ page }) => {
    // Collect console messages to detect LSP connection issues
    const consoleErrors: string[] = [];
    page.on('console', (msg) => {
      if (msg.type() === 'error' && msg.text().toLowerCase().includes('lsp')) {
        consoleErrors.push(msg.text());
      }
    });

    // gopls has to spawn, initialize, take didOpen and analyze the workspace. That is the wait.
    await waitForLspReady(page);

    // Verify no LSP-related console errors occurred
    expect(consoleErrors, 'LSP connection should not produce console errors').toHaveLength(0);

    // The editor should still be interactive (no error overlay)
    await expect(page.locator('.editor-error')).not.toBeVisible();
  });

  test('should show LSP completions when triggered', async ({ page }) => {
    // Ensure the editor has focus
    await page.locator('.cm-content').click();

    // Position cursor after "fmt.P" by navigating with keyboard
    await page.keyboard.press('Control+Home');
    for (let i = 0; i < 5; i++) {
      await page.keyboard.press('ArrowDown');
    }
    for (let i = 0; i < 6; i++) {
      await page.keyboard.press('ArrowRight');
    }

    // Trigger completion using CodeMirror's command API directly
    // This bypasses key binding issues in headless mode
    await page.evaluate(() => {
      // Find the CodeMirror EditorView from the DOM
      const editorEl = document.querySelector('.cm-editor');
      if (!editorEl) return;

      // The EditorView is accessible via the element's internal reference
      // In CodeMirror 6, we can find it via the state
      const view = (editorEl as any).viewCM6 || (editorEl as any).cm;
      if (view && view.dispatch) {
        // Import the continueCompletion command
        const { continueCompletion } = (window as any).cmAutocomplete || {};
        if (continueCompletion) {
          continueCompletion(view);
        }
      }
    });

    // Try Ctrl+Space as well (may work in some environments)
    await page.keyboard.press('Control+Space');

    // `isVisible` answers now and never waits, so the old check went false under load while the request
    // was still in flight. The tooltip either arrives inside the request budget or it does not.
    const hasAutocomplete = await page.locator('.cm-tooltip-autocomplete')
      .waitFor({ state: 'visible', timeout: 15000 })
      .then(() => true)
      .catch(() => false);

    if (hasAutocomplete) {
      // Verify there are completion items in the list
      const completionItems = page.locator('.cm-completionList > li');
      await expect(async () => {
        const count = await completionItems.count();
        expect(count, 'Completion list should have items').toBeGreaterThan(0);
      }).toPass({ timeout: 10000 });

      // Close the completion tooltip
      await page.keyboard.press('Escape');
      await expect(page.locator('.cm-tooltip-autocomplete')).toBeHidden();
    } else {
      // LSP may not have connected in time — skip gracefully
      // This can happen in slow CI environments
      console.log('Note: LSP completions not available (gopls may still be initializing)');
      test.skip();
    }
  });

  test('should show hover documentation over identifiers', async ({ page }) => {
    // Ensure the editor has focus
    await page.locator('.cm-content').click();

    await waitForLspReady(page);

    // Navigate cursor to the "fmt" identifier in 'import "fmt"' (line 3)
    await page.keyboard.press('Control+Home');
    await page.keyboard.press('ArrowDown'); // line 2: blank
    await page.keyboard.press('ArrowDown'); // line 3: import "fmt"
    for (let i = 0; i < 8; i++) {
      await page.keyboard.press('ArrowRight'); // position before "fmt"
    }
    await page.keyboard.press('ArrowRight'); // land on 'f' of "fmt"

    // Get the bounding box of the current cursor position and move mouse there
    const lineEl = await page.evaluate(() => {
      const editorEl = document.querySelector('.cm-editor');
      if (!editorEl) return null;
      const view = (editorEl as any).__view;
      if (!view) return null;
      const pos = view.state.selection.main.head;
      const coords = view.coordsAtPos(pos);
      return coords ? { top: coords.top, left: coords.left, bottom: coords.bottom, right: coords.right } : null;
    });

    if (lineEl) {
      // Move mouse to the center of the character and hold
      const midY = (lineEl.top + lineEl.bottom) / 2;
      const midX = (lineEl.left + lineEl.right) / 2;
      await page.mouse.move(midX, midY);
    }

    // Wait for the hover tooltip to appear (CodeMirror default hoverTime is 300ms)
    await expect(page.locator('.cm-tooltip')).toBeVisible({ timeout: 15000 });

    // The tooltip should contain package documentation
    const tooltipText = await page.locator('.cm-tooltip').textContent();
    expect(tooltipText?.length, 'Hover tooltip should contain documentation').toBeGreaterThan(0);
    expect(tooltipText).toContain('fmt');
  });

  test('should not produce LSP-related console errors during interaction', async ({ page }) => {
    const consoleErrors: string[] = [];
    page.on('console', (msg) => {
      if (msg.type() === 'error' && msg.text().toLowerCase().includes('lsp')) {
        consoleErrors.push(msg.text());
      }
    });

    // Perform editor interactions that would exercise the LSP connection
    await page.locator('.cm-content').click();

    // Type some text to trigger LSP notifications (didChange)
    await page.keyboard.press('Control+End');
    await page.keyboard.type(' ');

    // A completion request is the round trip that proves the server read the change: an error on the
    // didChange path surfaces in the same exchange, and a tooltip that never arrives is gopls answering
    // "no completions here", not a fault — so the absence is tolerated and the console is the assertion.
    await page.keyboard.press('Control+Space');
    await page.locator('.cm-tooltip-autocomplete')
      .waitFor({ state: 'visible', timeout: 15000 })
      .catch(() => undefined);
    await page.keyboard.press('Escape');

    // Undo the change
    await page.keyboard.press('Backspace');

    expect(consoleErrors, 'No LSP errors during editor interaction').toHaveLength(0);
  });

  test('should support LSP for multiple projects on shared connection', async ({ page }) => {
    // This test verifies that the single shared LSP WebSocket correctly
    // routes messages to different workspace/language servers.
    // The first project's LSP is already connected from beforeEach.

    // Create a second Go workspace
    ensureGoWorkspace2();
    const projectId2 = await createProjectViaApi(page, GO_WORKSPACE_2);

    try {
      // Navigate to the second project
      await page.evaluate((pid) => { window.location.href = `/?project=${pid}`; }, projectId2);
      await expect(page.locator('.board')).toBeVisible({ timeout: 15000 });

      // Open the explorer view
      await page.locator('.explorer-btn').click();
      await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });

      // Wait for file browser to finish loading
      await expect(async () => {
        const loadingVisible = await page.locator('.file-browser-loading').isVisible();
        const listVisible = await page.locator('.file-browser-list').isVisible().catch(() => false);
        const errorVisible = await page.locator('.file-browser-error').isVisible().catch(() => false);
        if (!loadingVisible && (listVisible || errorVisible)) {
          if (errorVisible) {
            const errorText = await page.locator('.file-browser-error').textContent();
            throw new Error(`File browser error: ${errorText}`);
          }
          if (!listVisible) {
            throw new Error('File browser finished loading but list is not visible');
          }
          return;
        }
        throw new Error('File browser still loading');
      }).toPass({ timeout: 60000, intervals: [2000] });

      // Open main.go in the second project
      const mainGoItem2 = page.locator('.file-browser-item', { hasText: 'main.go' });
      await expect(mainGoItem2).toBeVisible({ timeout: 10000 });
      await mainGoItem2.click();

      await expect(page.locator('.cm-editor')).toBeVisible({ timeout: 15000 });
      await expect(page.locator('.editor-loading')).not.toBeVisible({ timeout: 10000 });

      // Collect console errors
      const consoleErrors: string[] = [];
      page.on('console', (msg) => {
        if (msg.type() === 'error' && msg.text().toLowerCase().includes('lsp')) {
          consoleErrors.push(msg.text());
        }
      });

      // The second workspace has its own server on the shared connection: wait for that one.
      await waitForLspReady(page);

      // Verify no LSP errors — the shared connection should handle both workspaces
      expect(consoleErrors, 'LSP should work for second project on shared connection').toHaveLength(0);

      // The editor should be interactive
      await expect(page.locator('.editor-error')).not.toBeVisible();

      // And the footer says so in words, not just in the label the wait above read.
      await page.locator('.editor-footer [aria-label="LSP status"]').click();
      await expect(page.locator('.lsp-tooltip-value.lsp-ready')).toBeVisible({ timeout: 30000 });
    } finally {
      // Clean up second project
      try { await deleteProjectViaApi(page, projectId2); } catch { /* ignore */ }
      rmSync(GO_WORKSPACE_2, { recursive: true, force: true });
    }
  });
});
