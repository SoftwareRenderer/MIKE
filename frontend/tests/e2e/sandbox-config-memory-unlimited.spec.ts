import { test, expect, type Browser, type Page } from '@playwright/test';
import { csrfFetch, getProjectId } from './helpers';

// A blank Memory Limit in Sandbox Configuration means "no limit": the modal must
// send an explicit 0 (a null/omitted field tells the server to keep the current
// limit), and an unlimited project must render as a blank field, not "0 MB".
//
// Runs in the `isolated` Playwright project (see playwright.config.ts): it edits
// the project's sandbox config, which parallel main-project suites would clobber.
const STORAGE_STATE = '/tmp/kanban-test-storage-state.json.isolated';

const MEMORY_INPUT = 'input[aria-label="Maximum container memory in MB"]';
const SAVE_BUTTON = '[role="dialog"] .btn-primary';
const SEED_LIMIT_MB = 1536;

// `page` fixtures aren't available in beforeAll/afterAll — use a dedicated
// context with the logged-in storage state (same pattern as sandbox-memory.spec.ts).
async function setupPage(browser: Browser) {
  const context = await browser.newContext({ storageState: STORAGE_STATE });
  const page = await context.newPage();
  await page.goto('/');
  return { context, page };
}

async function fetchProject(page: Page, projectId: string) {
  return page.evaluate(async (pid) => {
    const res = await fetch(`/api/projects/${pid}`);
    return res.ok ? res.json() : null;
  }, projectId);
}

/** Sandbox config changes are 409 while tasks are In Progress — clear them first. */
async function clearInProgressTasks(page: Page, projectId: string) {
  for (let attempt = 0; attempt < 5; attempt++) {
    const tasks = await page.evaluate(async (pid) => {
      const res = await fetch(`/api/projects/${pid}/tasks`);
      return res.ok ? res.json() : [];
    }, projectId);
    let changed = false;
    for (const task of tasks) {
      if (task.status === 'in_progress') {
        await csrfFetch(page, `/api/projects/${projectId}/tasks/${task.id}/stop`, { method: 'POST' }).catch(() => {});
        await csrfFetch(page, `/api/projects/${projectId}/tasks/${task.id}/move`, {
          method: 'POST',
          body: { status: 'backlog' },
        }).catch(() => {});
        changed = true;
      }
    }
    if (!changed) return;
    await page.waitForTimeout(1000);
  }
}

/** Open the modal, put `value` in the memory field, save, and wait for it to close. */
async function saveMemoryLimit(page: Page, value: string) {
  await page.goto('/');
  await page.waitForSelector('.project-item', { timeout: 10000 });
  await page.click('.sandbox-config-btn');
  await expect(page.locator('.config-options')).toBeVisible({ timeout: 5000 });

  const input = page.locator(MEMORY_INPUT);
  await expect(input).toBeVisible();
  await input.fill(value);
  await page.locator(SAVE_BUTTON).click();
  await expect(page.locator('[role="dialog"]')).toHaveCount(0, { timeout: 5000 });
}

test.describe.serial('Sandbox Memory Limit — blank means no limit', () => {
  let projectId = '';
  let originalMemoryLimit: number | null = null;
  let skipReason = '';

  test.beforeAll(async ({ browser }) => {
    const { context, page } = await setupPage(browser);
    try {
      await page.waitForSelector('.project-item', { timeout: 10000 });
      projectId = await getProjectId(page);
      if (!projectId) {
        skipReason = 'no test project';
        return;
      }
      originalMemoryLimit = (await fetchProject(page, projectId))?.sandbox_memory_limit ?? null;

      await clearInProgressTasks(page, projectId);
      const put = await csrfFetch(page, `/api/projects/${projectId}`, {
        method: 'PUT',
        body: {
          sandbox_enabled: true,
          sandbox_network_isolation: false,
          sandbox_memory_limit: SEED_LIMIT_MB,
        },
      });
      if (!put.ok) {
        skipReason = `cannot seed sandbox memory limit (HTTP ${put.status})`;
        return;
      }
      // The modal is read-only while tasks are In Progress — bail out if locked.
      await page.reload();
      await page.waitForSelector('.project-item', { timeout: 10000 });
      await page.click('.sandbox-config-btn');
      await page.waitForSelector('.config-options, .locked-view', { timeout: 5000 });
      if (await page.locator('.locked-view').isVisible().catch(() => false)) {
        skipReason = 'sandbox config modal is locked by in-progress tasks';
      }
      await page.click('.overlay', { position: { x: 10, y: 10 } });
    } finally {
      await context.close();
    }
  });

  test.afterAll(async ({ browser }) => {
    if (!projectId) return;
    const { context, page } = await setupPage(browser);
    try {
      // Best-effort restore so sibling suites keep the project's limit.
      await csrfFetch(page, `/api/projects/${projectId}`, {
        method: 'PUT',
        body: { sandbox_memory_limit: originalMemoryLimit ?? 1024 },
      }).catch(() => {});
    } finally {
      await context.close();
    }
  });

  test('shows the seeded limit in the field', async ({ page }) => {
    if (skipReason) test.skip(true, skipReason);
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    await page.click('.sandbox-config-btn');
    await expect(page.locator('.config-options')).toBeVisible({ timeout: 5000 });

    await expect(page.locator(MEMORY_INPUT)).toHaveValue(String(SEED_LIMIT_MB));
  });

  test('saving a blank limit stores 0 (no limit) on the server', async ({ page }) => {
    if (skipReason) test.skip(true, skipReason);
    await saveMemoryLimit(page, '');

    await expect(async () => {
      const project = await fetchProject(page, projectId);
      expect(project?.sandbox_memory_limit).toBe(0);
    }).toPass({ timeout: 5000 });
  });

  test('an unlimited project reopens with a blank field and a "No limit" placeholder', async ({ page }) => {
    if (skipReason) test.skip(true, skipReason);
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    await page.click('.sandbox-config-btn');
    await expect(page.locator('.config-options')).toBeVisible({ timeout: 5000 });

    const input = page.locator(MEMORY_INPUT);
    await expect(input).toHaveValue('');
    await expect(input).toHaveAttribute('placeholder', 'No limit');
  });

  test('saving a blank limit again keeps it unlimited', async ({ page }) => {
    if (skipReason) test.skip(true, skipReason);
    await saveMemoryLimit(page, '');

    const project = await fetchProject(page, projectId);
    expect(project?.sandbox_memory_limit).toBe(0);
  });

  test('a negative value is saved as no limit', async ({ page }) => {
    if (skipReason) test.skip(true, skipReason);
    await saveMemoryLimit(page, String(SEED_LIMIT_MB));
    await saveMemoryLimit(page, '-512');

    await expect(async () => {
      const project = await fetchProject(page, projectId);
      expect(project?.sandbox_memory_limit).toBe(0);
    }).toPass({ timeout: 5000 });
  });

  test('a fresh limit restores enforcement', async ({ page }) => {
    if (skipReason) test.skip(true, skipReason);
    await saveMemoryLimit(page, '2048');

    await expect(async () => {
      const project = await fetchProject(page, projectId);
      expect(project?.sandbox_memory_limit).toBe(2048);
    }).toPass({ timeout: 5000 });
  });
});
