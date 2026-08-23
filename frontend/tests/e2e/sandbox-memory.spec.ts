import { execSync } from 'node:child_process';
import { test, expect, type Browser, type Page } from '@playwright/test';
import { createTaskViaApi, csrfFetch, getProjectId, navigateToTask } from './helpers';

// Memory limit (MB) applied to the project for this suite. The memhog probe
// allocates past it so the cgroup OOM killer fires — the enforced limit is
// proven by TestMemoryLimitEnforced in backend/sandbox/sandbox_test.go.
const MEMORY_LIMIT_MB = 512;

// Task prompt keywords that route the mock LLM (frontend/tests/mock-llm.js)
// to the sandbox probe fixtures on the task's first LLM call.
const SLEEP_PROBE = 'sandbox-sleep-probe';
const MEMHOG_PROBE = 'sandbox-memhog-probe';

// Logged-in storage state for the ISOLATED test server (written by global-setup).
// This suite runs in the `isolated` Playwright project (see playwright.config.ts):
// it modifies the project's sandbox config and depends on its probe task staying
// in_progress, which parallel main-project suites would clobber.
const STORAGE_STATE = '/tmp/kanban-test-storage-state.json.isolated';

// `page` fixtures are not available in beforeAll/afterAll — create a dedicated
// context with the logged-in storage state for setup/teardown (same pattern as
// editor-wrap-highlight.spec.ts).
async function setupPage(browser: Browser) {
  const context = await browser.newContext({ storageState: STORAGE_STATE });
  const page = await context.newPage();
  await page.goto('/');
  return { context, page };
}

/**
 * Create a task whose prompt routes the mock LLM to a sandbox probe fixture,
 * then start it for real — POST /start runs the executor, which creates the
 * sandbox container and executes the fixture's bash tool call inside it.
 */
async function startProbeTask(page: Page, projectId: string, keyword: string): Promise<string> {
  const taskId = await createTaskViaApi(page, projectId, `${keyword}-${Date.now()}: run the probe command`);
  const start = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/start`, { method: 'POST' });
  expect(start.ok).toBe(true);
  return taskId;
}

test.describe.serial('Sandbox Memory Tracking', () => {
  let projectId = '';
  let sleepTaskId = '';
  let memhogTaskId = '';
  let originalMemoryLimit: number | null = null;
  let skipReason = '';

  test.beforeAll(async ({ browser }) => {
    // gVisor probe — mirrors SandboxManager.checkAvailable (runsc on PATH).
    try {
      execSync('runsc --version', { stdio: 'ignore' });
    } catch {
      skipReason = 'gVisor (runsc) not available';
      return;
    }

    const { context, page } = await setupPage(browser);
    try {
      await page.waitForSelector('.project-item', { timeout: 10000 });
      projectId = await getProjectId(page);

      const project = await page.evaluate(async (pid) => {
        const res = await fetch(`/api/projects/${pid}`);
        return res.ok ? res.json() : null;
      }, projectId);
      originalMemoryLimit = project?.sandbox_memory_limit ?? 1024;

      // Sandbox config changes are blocked while tasks are in_progress — stop
      // any first (same pattern as sandbox-forward-ports.spec.ts).
      for (let attempt = 0; attempt < 5; attempt++) {
        const tasks = await page.evaluate(async (pid) => {
          const res = await fetch(`/api/projects/${pid}/tasks`);
          return res.ok ? res.json() : [];
        }, projectId);
        let changed = false;
        for (const task of tasks) {
          if (task.status === 'in_progress') {
            await csrfFetch(page, `/api/projects/${projectId}/tasks/${task.id}/stop`, {
              method: 'POST',
            }).catch(() => {}); // Ignore if task not running
            await csrfFetch(page, `/api/projects/${projectId}/tasks/${task.id}/move`, {
              method: 'POST',
              body: { status: 'backlog' },
            }).catch(() => {}); // Ignore if already moved
            changed = true;
          }
        }
        if (!changed) break;
        await page.waitForTimeout(1000);
      }

      const put = await csrfFetch(page, `/api/projects/${projectId}`, {
        method: 'PUT',
        body: { sandbox_enabled: true, sandbox_memory_limit: MEMORY_LIMIT_MB },
      });
      if (!put.ok) {
        skipReason = `cannot set sandbox memory limit (HTTP ${put.status})`;
      }
    } finally {
      await context.close();
    }
  });

  test.afterAll(async ({ browser }) => {
    if (!projectId) return;
    const { context, page } = await setupPage(browser);
    try {
      // Restore the project's memory limit (best-effort).
      await csrfFetch(page, `/api/projects/${projectId}`, {
        method: 'PUT',
        body: { sandbox_memory_limit: originalMemoryLimit },
      }).catch(() => {});
      // Best-effort cleanup of the probe tasks.
      for (const taskId of [sleepTaskId, memhogTaskId]) {
        if (taskId) {
          await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}`, {
            method: 'DELETE',
          }).catch(() => {});
        }
      }
    } finally {
      await context.close();
    }
  });

  test('shows live memory usage in the sandbox panel while the task runs (no dot when healthy)', async ({ page }) => {
    if (skipReason) test.skip(true, skipReason);
    test.setTimeout(120000);

    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });

    sleepTaskId = await startProbeTask(page, projectId, SLEEP_PROBE);
    await navigateToTask(page, sleepTaskId, projectId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // The mock LLM's bash tool call (sleep 20) runs in the sandbox container.
    await expect(page.locator('.tool-call-card').first()).toBeVisible({ timeout: 60000 });

    // Open the "Sandboxed Task" panel and wait for the live memory row.
    await page.click('.sandbox-btn');
    await expect(page.locator('.memory-value')).toBeVisible({ timeout: 30000 });

    // Usage is reported against the 512 MB limit and > 0 while running.
    await expect(async () => {
      const text = await page.locator('.memory-value').innerText();
      expect(text).toContain(`/ ${MEMORY_LIMIT_MB} MB`);
      expect(parseInt(text.split(' / ')[0], 10)).toBeGreaterThan(0);
    }).toPass({ timeout: 30000 });

    // Healthy container (far below 80% of the limit): no warning dot.
    await expect(page.locator('.mem-dot')).not.toBeAttached();

    // Close the panel.
    await page.click('.overlay', { position: { x: 10, y: 10 } });
    await expect(page.locator('[role="dialog"]')).not.toBeVisible({ timeout: 5000 });
  });

  test('shows the warning dot and OOM notice when the container hits its memory limit', async ({ page }) => {
    if (skipReason) test.skip(true, skipReason);
    test.setTimeout(180000);

    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });

    memhogTaskId = await startProbeTask(page, projectId, MEMHOG_PROBE);
    await navigateToTask(page, memhogTaskId, projectId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // The probe's bash command allocates past the limit: usage crosses 80%
    // (near-limit dot) and the cgroup OOM killer fires — the oom_kills
    // condition keeps the dot lit even after usage drops.
    await expect(page.locator('.tool-call-card').first()).toBeVisible({ timeout: 60000 });
    await expect(page.locator('.mem-dot')).toBeVisible({ timeout: 90000 });

    // The tooltip carries the live usage and the OOM kill count.
    await expect(async () => {
      const title = (await page.locator('.sandbox-btn').getAttribute('title')) || '';
      expect(title).toContain('OOM kill');
    }).toPass({ timeout: 30000 });

    // The panel shows the OOM warning notice.
    await page.click('.sandbox-btn');
    await expect(page.locator('.oom-warning')).toBeVisible({ timeout: 15000 });
    await expect(page.locator('.oom-warning')).toContainText('OOM-killed');
  });

  test('keeps the most recent usage after the task goes idle and the page reloads', async ({ page }) => {
    if (skipReason) test.skip(true, skipReason);
    test.setTimeout(120000);

    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });

    // Move the memhog task out of in_progress — the sampler stops pushing,
    // and the server keeps the last sample for late subscribers.
    const move = await csrfFetch(page, `/api/projects/${projectId}/tasks/${memhogTaskId}/move`, {
      method: 'POST',
      body: { status: 'review' },
    });
    expect(move.ok).toBe(true);

    // Reload into the task view — the fresh WS subscription must be served the
    // most recent stats (the client store was wiped by the reload).
    await navigateToTask(page, memhogTaskId, projectId);

    // The OOM kill count keeps the dot lit even though the task is idle.
    await expect(page.locator('.mem-dot')).toBeVisible({ timeout: 30000 });

    // The panel still shows the most recent usage and the OOM warning.
    await page.click('.sandbox-btn');
    await expect(page.locator('.memory-value')).toBeVisible({ timeout: 15000 });
    await expect(page.locator('.memory-value')).toContainText(`/ ${MEMORY_LIMIT_MB} MB`);
    await expect(page.locator('.oom-warning')).toBeVisible();

    // Peak-reached condition (cgroup memory.peak, kernels 5.19+): after the
    // OOM the most recent sample carries a peak at/above the limit, so the
    // dot is also lit via the peak condition — independently of the OOM
    // counter — and the tooltip names it. `.memory-peak` renders only when
    // peak data exists; older kernels (peak 0) skip the assertion.
    const peakEl = page.locator('.memory-peak');
    if (await peakEl.isVisible()) {
      const peakMB = parseInt((await peakEl.innerText()).replace(/[^0-9]/g, ''), 10);
      expect(peakMB).toBeGreaterThanOrEqual(MEMORY_LIMIT_MB);
      const title = (await page.locator('.sandbox-btn').getAttribute('title')) || '';
      expect(title).toContain('peak reached limit');
    }
  });
});
