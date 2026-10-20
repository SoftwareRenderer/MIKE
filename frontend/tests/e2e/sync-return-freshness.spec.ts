import { test, expect, type Page } from '@playwright/test';
import { execSync } from 'child_process';
import { writeFileSync } from 'fs';
import {
  isolatedProject, createTaskViaApi, navigateToTask, csrfFetch, waitForWorktree,
  getWorktreePath, ownProjectId, apiGetJson, stopReading, comeBack, sendTurn,
} from './helpers';

// Each test file gets its own project: see isolatedProject() in helpers.ts.
isolatedProject();

/**
 * A task open in a window that stopped being read, coming back with the server's answer rather than the screen
 * it left behind. Both views ask the sync digest instead of judging their own staleness.
 * `docs/reconnection.md → The tab that comes back`.
 */

/** One newest-window read, as the recording holds it: how many digests had been asked when it went out. */
/** While `__away`, every frame but the keepalive is dropped before the app sees it. While `__unfocused`, the
 * page reports no focus — the window on another desktop, never hidden, so no `visibilitychange` fires. */
async function watchTheReturn(page: Page): Promise<void> {
  await page.addInitScript(() => {
    const w = window as any;
    w.__away = false;
    w.__unfocused = false;
    w.__syncCalls = 0;
    w.__windowReads = [] as number[];
    Object.defineProperty(document, 'hasFocus', {
      configurable: true,
      get: () => () => !w.__unfocused,
    });
    const realFetch = window.fetch;
    window.fetch = ((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input
        : input instanceof URL ? input.href : (input as Request).url;
      if (url.includes('/sync')) w.__syncCalls++;
      if (url.includes('/messages')) w.__windowReads.push(w.__syncCalls);
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

/** The digest the server holds, read out of band so the test knows the server noticed before the return. */
async function digest(page: Page, projectId: string, taskId: string) {
  return apiGetJson<{ diff: { known: boolean; hash?: string } }>(
    page, `/api/projects/${projectId}/tasks/${taskId}/sync`);
}

/**
 * The return gesture, pressed until the screen acts on it. A return signal is deduped for
 * `RETURN_DEDUPE_MS` — 2 s, `$lib/utils/tab-return.ts` — so one synthetic `focus` can land inside a
 * reconcile the screen just did and be refused outright; a reader who clicks the window twice is this
 * gesture twice. `why` answers `'ok'` when the claim holds and names what is missing otherwise, so a red
 * says which half never arrived rather than only that a ceiling was reached.
 */
async function comeBackUntil(page: Page, why: () => Promise<string>): Promise<void> {
  await expect.poll(async () => {
    await comeBack(page);
    return why();
  }, { timeout: 20_000 }).toBe('ok');
}

/** The screen's own recording: how many digests it asked, and when each newest-window read went out. */
async function recording(page: Page) {
  return page.evaluate(() => ({
    sync: (window as any).__syncCalls as number,
    reads: (window as any).__windowReads as number[],
  }));
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
    // A worktree that will not appear is the failure, not an environment to work around: the task was
    // moved to in_progress immediately above, and every later line of this test depends on that directory.
    const worktree = await waitForWorktree(projectId, taskId);
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

    // What the screen had applied is not the test's premise to hold. A page the lane starves reconciles on
    // its own clock — the suspension trigger reads a starved timer as a wake — so a claim about which
    // baseline a later read may name is a claim about the machine, and that is what flaked here. What the
    // machine cannot spare the screen is the fetch: the digest asked, a window read following it.
    stopReading(page);
    const content = `written while unread ${unique}`;
    await sendTurn(page, projectId, taskId, content);
    const held = await recording(page);

    await comeBackUntil(page, async () => {
      const now = await recording(page);
      if (now.sync === held.sync) return 'no digest was asked';
      // A read belongs to this return because the hook stamps it with the digests asked when it went out.
      return now.reads.some(digests => digests > held.sync)
        ? 'ok' : `digest asked, no window read issued after it (${now.reads.length} read(s) recorded)`;
    });

    // The row is the half with teeth: a return that reads nothing leaves it away whatever the counters say,
    // and this is what the person came back to see.
    await expect(page.locator('.chat-message', { hasText: content })).toBeVisible({ timeout: 20_000 });
  });
});
