import { test, expect, type Page } from '@playwright/test';
import { execSync } from 'child_process';
import { writeFileSync } from 'fs';
import {
  isolatedProject, createTaskViaApi, navigateToTask, csrfFetch, waitForWorktree,
  getWorktreePath, ownProjectId, apiGetJson,
} from './helpers';

// Each test file gets its own project: see isolatedProject() in helpers.ts.
isolatedProject();

/**
 * A task open in a window that stopped being read, coming back with the server's answer rather than the screen
 * it left behind. Both views ask the sync digest instead of judging their own staleness.
 * `docs/reconnection.md → The tab that comes back`.
 */

/** While `__away`, every frame but the keepalive is dropped before the app sees it. While `__unfocused`, the
 * page reports no focus — the window on another desktop, never hidden, so no `visibilitychange` fires. */
async function watchTheReturn(page: Page): Promise<void> {
  await page.addInitScript(() => {
    const w = window as any;
    w.__away = false;
    w.__unfocused = false;
    w.__syncCalls = 0;
    w.__revisions = [] as Array<string | null>;
    Object.defineProperty(document, 'hasFocus', {
      configurable: true,
      get: () => () => !w.__unfocused,
    });
    const realFetch = window.fetch;
    window.fetch = ((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input
        : input instanceof URL ? input.href : (input as Request).url;
      if (url.includes('/sync')) w.__syncCalls++;
      if (url.includes('/messages')) {
        w.__revisions.push(new Headers(init?.headers).get('If-None-Match'));
      }
      return realFetch(input as any, init);
    }) as typeof window.fetch;
    const RealWebSocket = window.WebSocket;
    window.WebSocket = class extends RealWebSocket {
      constructor(url: string | URL, protocols?: string | string[]) {
        super(url, protocols);
        if (!String(url).includes('/api/stream')) return;
        this.addEventListener('message', (e: MessageEvent) => {
          if (!w.__away) return;
          let frame: any;
          try { frame = JSON.parse(String(e.data)); } catch { return; }
          if (frame?.type === 'ping') return;
          e.stopImmediatePropagation();
        });
      }
    } as typeof WebSocket;
  });
}

const stopReading = (page: Page) =>
  page.evaluate(() => { (window as any).__unfocused = true; (window as any).__away = true; });

/** The window comes back. Only `focus` is dispatched — it was never hidden. */
const comeBack = (page: Page) => page.evaluate(() => {
  const w = window as any;
  w.__away = false;
  w.__unfocused = false;
  window.dispatchEvent(new Event('focus'));
});

/** The digest the server holds, read out of band so the test knows the server noticed before the return. */
async function digest(page: Page, projectId: string, taskId: string) {
  return apiGetJson<{ diff: { known: boolean; hash?: string } }>(
    page, `/api/projects/${projectId}/tasks/${taskId}/sync`);
}

test.describe('the window that comes back', () => {
  test('brings the diff of a change made while it was elsewhere, keeping the open file open', async ({ page }) => {
    await watchTheReturn(page);
    const projectId = ownProjectId();
    const unique = Date.now();
    const openName = `sync-open-${unique}.txt`;
    const awayName = `sync-away-${unique}.txt`;

    const taskId = await createTaskViaApi(page, projectId, `sync-return-${unique}`);
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST', body: { status: 'in_progress' },
    });
    let worktree = '';
    try {
      worktree = await waitForWorktree(projectId, taskId);
    } catch {
      test.skip(); // no worktree in this environment: there is no diff to be stale about
      return;
    }
    // A tracked file: the case whose rows the refresh must keep. An untracked file's preview is fetched on
    // demand.
    writeFileSync(`${getWorktreePath(projectId, taskId)}/${openName}`, `open before leaving ${unique}\n`);
    execSync(`git -C ${getWorktreePath(projectId, taskId)} add ${openName}`);

    await navigateToTask(page, taskId, projectId);
    const openRow = page.locator('.diff-file-section', { hasText: openName }).first();
    await expect(openRow).toBeVisible({ timeout: 20_000 });
    // Scoped to its own section: every file in the manifest renders its rows inline, so a bare
    // `.diff-file-content` names whichever one sorts first.
    const openContent = page.locator('.diff-file-section', { hasText: openName }).first();
    await expect(openContent).toContainText(`open before leaving ${unique}`, { timeout: 20_000 });

    // The window stops being read, and the work changes while nobody is looking.
    stopReading(page);
    writeFileSync(`${worktree}/${awayName}`, `written while away ${unique}\n`);
    await expect.poll(async () => (await digest(page, projectId, taskId)).diff.known, { timeout: 20_000 })
      .toBe(true);
    const hashWhileAway = (await digest(page, projectId, taskId)).diff.hash;

    const syncBefore = await page.evaluate(() => (window as any).__syncCalls as number);
    await comeBack(page);

    // The digest is what was asked, not the panel guessing from the state it holds.
    await expect.poll(async () => (await page.evaluate(() => (window as any).__syncCalls as number)) > syncBefore)
      .toBe(true);
    await expect(page.locator('.diff-file-section', { hasText: awayName }).first()).toBeVisible({ timeout: 20_000 });
    // The file the reader was in is still drawn where they left it.
    await expect(openContent).toContainText(`open before leaving ${unique}`);
    expect(hashWhileAway).toBeTruthy();
  });

  test('asks the digest rather than trusting the baseline it holds', async ({ page }) => {
    await watchTheReturn(page);
    const projectId = ownProjectId();
    const unique = Date.now();
    const taskId = await createTaskViaApi(page, projectId, `sync-baseline-${unique}`);
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST', body: { status: 'in_progress' },
    });

    await navigateToTask(page, taskId, projectId);
    await expect(page.locator('.chat-form')).toBeVisible({ timeout: 20_000 });

    const before = await page.evaluate(() => ({
      sync: (window as any).__syncCalls as number,
      reads: (window as any).__revisions.length as number,
    }));
    await comeBack(page);

    await expect.poll(async () => {
      const now = await page.evaluate(() => ({
        sync: (window as any).__syncCalls as number,
        reads: (window as any).__revisions as Array<string | null>,
      }));
      // One digest, and the window read that follows it names no baseline: this screen has applied nothing it
      // can name.
      return now.sync > before.sync && now.reads.length > before.reads
        && now.reads.slice(before.reads).every(v => v === null);
    }, { timeout: 20_000 }).toBe(true);
  });
});
