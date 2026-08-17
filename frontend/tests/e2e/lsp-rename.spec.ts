import { test, expect } from '@playwright/test';
import { execSync } from 'node:child_process';
import { mkdirSync, writeFileSync, rmSync, existsSync, readFileSync } from 'node:fs';
import { csrfFetch } from './helpers';

// ─── Prerequisite check (mirrors lsp-integration.spec.ts) ───────────────────

const hasGopls = (() => {
  try { execSync('which gopls', { stdio: 'pipe' }); return true; }
  catch { return false; }
})();

test.skip(!hasGopls, 'gopls not installed');

// ─── Test workspace ──────────────────────────────────────────────────────────

const GO_RENAME_WORKSPACE = '/tmp/kanban-test-go-rename';

function ensureGoRenameWorkspace(): void {
  if (!existsSync(GO_RENAME_WORKSPACE)) {
    mkdirSync(GO_RENAME_WORKSPACE, { recursive: true });
    execSync('git init', { cwd: GO_RENAME_WORKSPACE });
    execSync('git -c user.email="test@kanban.local" -c user.name="Test" commit --allow-empty -m "init"', { cwd: GO_RENAME_WORKSPACE });
    execSync('go mod init lsprename', { cwd: GO_RENAME_WORKSPACE });
  }
  // Always write the test files (idempotent): the symbol spans two files so
  // a correct rename must touch both.
  writeFileSync(`${GO_RENAME_WORKSPACE}/main.go`, `package main

import "fmt"

func getGreeting() string {
\treturn "hello"
}

func main() {
\tfmt.Println(getGreeting())
}
`);
  writeFileSync(`${GO_RENAME_WORKSPACE}/helper.go`, `package main

import "strings"

func shout() string {
\treturn strings.ToUpper(getGreeting())
}
`);
}

// ─── Test workspace (TypeScript) ─────────────────────────────────────────────

const TS_RENAME_WORKSPACE = '/tmp/kanban-test-ts-rename';

function ensureTsRenameWorkspace(): void {
  if (!existsSync(TS_RENAME_WORKSPACE)) {
    mkdirSync(TS_RENAME_WORKSPACE, { recursive: true });
    execSync('git init', { cwd: TS_RENAME_WORKSPACE });
    execSync('git -c user.email="test@kanban.local" -c user.name="Test" commit --allow-empty -m "init"', { cwd: TS_RENAME_WORKSPACE });
  }
  // Two files sharing one symbol in a single tsconfig project (mirrors
  // backend/lsp/tsserver_integration_test.go) so a correct rename must touch both.
  writeFileSync(`${TS_RENAME_WORKSPACE}/tsconfig.json`, `{\n  "compilerOptions": {"target": "ES2022", "module": "ESNext", "moduleResolution": "bundler", "strict": true, "noEmit": true},\n  "include": ["main.ts", "helper.ts"]\n}\n`);
  writeFileSync(`${TS_RENAME_WORKSPACE}/main.ts`, `export function getGreeting(): string {
\treturn "hello";
}

export function exclaim(): string {
\treturn getGreeting() + "!";
}
`);
  writeFileSync(`${TS_RENAME_WORKSPACE}/helper.ts`, `import { getGreeting } from "./main";

export function shout(): string {
\treturn getGreeting().toUpperCase();
}
`);
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

async function ensureLspExtension(page: test['project']['use'], name: string): Promise<void> {
  const existing = await csrfFetch(page, '/api/extensions', { method: 'GET' });
  const list: any[] = Array.isArray(existing.data) ? existing.data : [];
  if (list.some((e) => e.name === name)) {
    return;
  }
  // Install from registry (SSE stream; csrfFetch resolves when the stream ends).
  const result = await csrfFetch(page, '/api/extensions/install', {
    method: 'POST',
    body: { name },
  });
  if (!result.ok) {
    throw new Error(`${name} extension install failed: ${result.status} ${JSON.stringify(result.data)}`);
  }
}

async function waitForTaskTerminalStatus(
  page: test['project']['use'],
  projectId: string,
  taskId: string,
  timeout = 90000
): Promise<string> {
  await expect
    .poll(
      async () => {
        const data = await page.evaluate(async ({ pid, tid }) => {
          const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
          return res.ok ? res.json() : null;
        }, { pid: projectId, tid: taskId });
        return data?.status ?? 'unknown';
      },
      { timeout, intervals: [2000] }
    )
    .not.toBe('in_progress');
  const data = await page.evaluate(async ({ pid, tid }) => {
    const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
    return res.ok ? res.json() : null;
  }, { pid: projectId, tid: taskId });
  return data?.status ?? 'unknown';
}

// ─── Test ────────────────────────────────────────────────────────────────────

test.describe.serial('LSP Rename (agent tool, mock LLM)', () => {
  test('should rename a Go symbol across files via lsp_rename', async ({ page }) => {
    test.setTimeout(300000); // first gopls image build can be slow

    ensureGoRenameWorkspace();

    // Register the workspace as a project
    const project = await csrfFetch(page, '/api/projects', {
      method: 'POST',
      body: { name: 'lsp-rename-test', path: GO_RENAME_WORKSPACE },
    });
    if (!project.ok) {
      throw new Error(`Failed to create project: ${project.status} ${JSON.stringify(project.data)}`);
    }
    const projectId = project.data.id;

    // Install the gopls extension (required for the LSP tools to exist)
    await ensureLspExtension(page, 'gopls');

    // Create a task whose prompt routes the mock LLM to the rename fixture
    const config = await page.evaluate(async () => {
      const res = await fetch('/api/config');
      return res.json();
    });
    const workerProvider = (config.providers ?? []).find(
      (p: any) => p.name === config.worker_provider
    );
    const task = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
      method: 'POST',
      body: {
        prompt: `rename-test ${Date.now()}: use lsp_rename to rename getGreeting to greet in main.go`,
        llama_server_url: workerProvider?.url || '',
        model_name: config?.worker_model || '',
      },
    });
    expect(task.ok).toBe(true);
    const taskId = task.data.id;
    expect(taskId).toBeTruthy();

    // Start the task
    const start = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/start`, {
      method: 'POST',
    });
    expect(start.ok).toBe(true);

    // Wait for the task to finish
    await waitForTaskTerminalStatus(page, projectId, taskId);

    // ── Assert: the rename landed in BOTH files on disk ──
    const mainGo = readFileSync(`${GO_RENAME_WORKSPACE}/main.go`, 'utf8');
    const helperGo = readFileSync(`${GO_RENAME_WORKSPACE}/helper.go`, 'utf8');
    for (const [name, content] of [['main.go', mainGo], ['helper.go', helperGo]]) {
      expect(content, `${name} should contain the new name`).toContain('greet');
      expect(content, `${name} should not contain the old name`).not.toContain('getGreeting');
    }

    // ── Assert: the rename tool call is visible in the chat UI ──
    await page.goto(`/`);
    await page.waitForSelector('.project-item', { timeout: 10000 });
    await page.evaluate((pid) => { window.location.href = `/?project=${pid}`; }, projectId);
    await expect(page.locator('.board')).toBeVisible({ timeout: 15000 });
    await page.evaluate((tid) => { window.location.href = `/?task=${tid}`; }, taskId);
    await expect(page.locator('.task-detail-page, .chat-container')).toBeVisible({ timeout: 15000 });
    await expect(page.locator('text=LSP Rename').first()).toBeVisible({ timeout: 30000 });

    // ── Cleanup ──
    await csrfFetch(page, `/api/projects/${projectId}`, { method: 'DELETE' });
    rmSync(GO_RENAME_WORKSPACE, { recursive: true, force: true });
  });

  test('should rename a TypeScript symbol across files via lsp_rename', async ({ page }) => {
    test.setTimeout(300000); // first tsserver image build can be slow

    ensureTsRenameWorkspace();

    // Register the workspace as a project
    const project = await csrfFetch(page, '/api/projects', {
      method: 'POST',
      body: { name: 'lsp-rename-ts-test', path: TS_RENAME_WORKSPACE },
    });
    if (!project.ok) {
      throw new Error(`Failed to create project: ${project.status} ${JSON.stringify(project.data)}`);
    }
    const projectId = project.data.id;

    // Install the TypeScript language server extension
    await ensureLspExtension(page, 'typescript-language-server');

    // Create a task whose prompt routes the mock LLM to the TS rename fixture
    const config = await page.evaluate(async () => {
      const res = await fetch('/api/config');
      return res.json();
    });
    const workerProvider = (config.providers ?? []).find(
      (p: any) => p.name === config.worker_provider
    );
    const task = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
      method: 'POST',
      body: {
        prompt: `rename-test-ts ${Date.now()}: use lsp_rename to rename getGreeting to greet in main.ts`,
        llama_server_url: workerProvider?.url || '',
        model_name: config?.worker_model || '',
      },
    });
    expect(task.ok).toBe(true);
    const taskId = task.data.id;
    expect(taskId).toBeTruthy();

    // Start the task
    const start = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/start`, {
      method: 'POST',
    });
    expect(start.ok).toBe(true);

    // Wait for the task to finish
    await waitForTaskTerminalStatus(page, projectId, taskId);

    // ── Assert: the rename landed in BOTH .ts files on disk ──
    const mainTs = readFileSync(`${TS_RENAME_WORKSPACE}/main.ts`, 'utf8');
    const helperTs = readFileSync(`${TS_RENAME_WORKSPACE}/helper.ts`, 'utf8');
    for (const [name, content] of [['main.ts', mainTs], ['helper.ts', helperTs]]) {
      expect(content, `${name} should not contain the old name`).not.toContain('getGreeting');
      expect(content, `${name} should contain the new name`).toContain('greet');
    }

    // ── Assert: the rename tool call is visible in the chat UI ──
    await page.goto(`/`);
    await page.waitForSelector('.project-item', { timeout: 10000 });
    await page.evaluate((pid) => { window.location.href = `/?project=${pid}`; }, projectId);
    await expect(page.locator('.board')).toBeVisible({ timeout: 15000 });
    await page.evaluate((tid) => { window.location.href = `/?task=${tid}`; }, taskId);
    await expect(page.locator('.task-detail-page, .chat-container')).toBeVisible({ timeout: 15000 });
    await expect(page.locator('text=LSP Rename').first()).toBeVisible({ timeout: 30000 });

    // ── Cleanup ──
    await csrfFetch(page, `/api/projects/${projectId}`, { method: 'DELETE' });
    rmSync(TS_RENAME_WORKSPACE, { recursive: true, force: true });
  });
});
