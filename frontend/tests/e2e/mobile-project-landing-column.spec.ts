import { test, expect, type Page } from '@playwright/test';
import { createTaskViaApi, waitForTaskState, csrfFetch } from './helpers';
import { execSync } from 'node:child_process';
import { rmSync } from 'node:fs';

// ============================================================
// MOBILE LANDING COLUMN ON PROJECT SWITCH
// ============================================================
// Switching to a different project on mobile shows the column that has work: Review,
// else In Progress, else Backlog. The first load never scrolls. The two projects live in
// their own temp repos so the shared test project is never touched.
// Tasks are parked in their column so nothing moves them: review/done moves never start
// execution, and the in_progress one is paused mid-stream ("stop-test slow-start").

const REPO_A = '/tmp/kanban-test-repo-landing-a';
const REPO_B = '/tmp/kanban-test-repo-landing-b';

function initRepo(path: string): void {
  rmSync(path, { recursive: true, force: true });
  execSync(`git init -q "${path}"`, { stdio: 'ignore' });
  // Commits need an identity; don't depend on the global git config.
  execSync('git config user.email "test@kanban.local"', { cwd: path, stdio: 'ignore' });
  execSync('git config user.name "Test"', { cwd: path, stdio: 'ignore' });
  execSync('git commit -q --allow-empty -m init', { cwd: path, stdio: 'ignore' });
}

/** Create a task and leave it sitting in `status`. */
async function parkTask(page: Page, projectId: string, prompt: string, status: string): Promise<void> {
  const id = await createTaskViaApi(page, projectId, prompt);
  const moved = await csrfFetch(page, `/api/projects/${projectId}/tasks/${id}/move`, {
    method: 'POST',
    body: { status },
  });
  if (!moved.ok) throw new Error(`move(${status}): ${JSON.stringify(moved.data)}`);
  if (status === 'in_progress') {
    // The move started the task — pause it while the slow stream is in flight.
    await waitForTaskState(
      page, projectId, id,
      t => t?.execution_state === 'running' || t?.execution_state === 'pulling_image',
      20000,
    );
    const stopped = await csrfFetch(page, `/api/projects/${projectId}/tasks/${id}/stop`, { method: 'POST' });
    if (!stopped.ok) throw new Error(`stop: ${JSON.stringify(stopped.data)}`);
  }
  await waitForTaskState(
    page, projectId, id,
    t => t?.status === status && (status !== 'in_progress' || t?.execution_state === 'paused'),
    20000,
  );
}

/** Scroll the board by hand, so the next landing is not the old position. */
async function scrollBoardTo(page: Page, title: string): Promise<void> {
  await page.evaluate((t) => {
    const cols = Array.from(document.querySelectorAll('.board .column')) as HTMLElement[];
    cols.find(c => c.getAttribute('aria-label') === `${t} column`)
      ?.scrollIntoView({ behavior: 'auto', inline: 'center', block: 'nearest' });
  }, title);
  await page.waitForTimeout(200);
}

/** What the board shows: the column at its centre, plus geometry for failure messages. */
async function boardSnapshot(page: Page): Promise<{ center: string; scrollLeft: number; clientWidth: number; cols: string[] }> {
  return page.evaluate(() => {
    const board = document.querySelector('.board') as HTMLElement;
    const mid = board.getBoundingClientRect().left + board.clientWidth / 2;
    let center = '';
    let bestDist = Infinity;
    const cols = Array.from(document.querySelectorAll('.board .column') as NodeListOf<HTMLElement>).map(el => {
      const r = el.getBoundingClientRect();
      const dist = Math.abs((r.left + r.right) / 2 - mid);
      if (dist < bestDist) {
        bestDist = dist;
        center = (el.getAttribute('aria-label') || '').replace(/ column$/, '');
      }
      return `${(el.getAttribute('aria-label') || '').replace(/ column$/, '')}: ${Math.round(r.left)}..${Math.round(r.right)}`;
    });
    return { center, scrollLeft: Math.round(board.scrollLeft), clientWidth: board.clientWidth, cols };
  });
}

/** Switch projects the way a mobile user does: hamburger → project row. */
async function openProject(page: Page, name: string): Promise<void> {
  await page.locator('.hamburger').click();
  await page.locator('.sidebar .project-item', { hasText: name }).click();
  await expect(page.locator('.sidebar.mobile-open')).toHaveCount(0);
  await expect(page.locator('.project-item-wrapper.active .project-item-content')).toContainText(name);
  await page.waitForTimeout(300); // let the scroll settle
}

test.describe.serial('Mobile landing column on project switch', () => {
  let projectA = '';
  let projectB = '';
  let createdHere = false;

  test.beforeAll(async ({ browser }) => {
    initRepo(REPO_A);
    initRepo(REPO_B);
    const ctx = await browser.newContext({ storageState: '/tmp/kanban-test-storage-state.json' });
    const page = await ctx.newPage();
    for (const [name, path] of [['landing-a', REPO_A], ['landing-b', REPO_B]] as const) {
      const res = await csrfFetch(page, '/api/projects', { method: 'POST', body: { name, path } });
      if (res.ok && res.data?.id) {
        if (name === 'landing-a') projectA = res.data.id; else projectB = res.data.id;
        createdHere = true;
        continue;
      }
      // Already present from an interrupted run — reuse it.
      const all = await page.evaluate(async () => (await fetch('/api/projects')).json());
      const id = all.find((p: any) => p.path === path)?.id || '';
      if (!id) throw new Error(`create project ${name}: ${JSON.stringify(res.data)}`);
      if (name === 'landing-a') projectA = id; else projectB = id;
    }
    await ctx.close();
  });

  test.afterAll(async ({ browser }) => {
    if (createdHere) {
      const ctx = await browser.newContext({ storageState: '/tmp/kanban-test-storage-state.json' });
      const page = await ctx.newPage();
      for (const id of [projectA, projectB]) {
        if (id) await csrfFetch(page, `/api/projects/${id}`, { method: 'DELETE' });
      }
      await ctx.close();
    }
    rmSync(REPO_A, { recursive: true, force: true });
    rmSync(REPO_B, { recursive: true, force: true });
  });

  test.beforeEach(async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto('/');
    await page.waitForSelector('.board .column', { timeout: 10000 });
  });

  test('lands on In Progress when tasks are in progress (and none in review)', async ({ page }) => {
    await parkTask(page, projectA, `stop-test slow-start: landing-ip ${Date.now()}`, 'in_progress');

    // Park on the other project, then switch to A: it must show In Progress.
    await openProject(page, 'landing-b');
    await openProject(page, 'landing-a');
    const snap = await boardSnapshot(page);
    expect(snap.center, JSON.stringify(snap)).toBe('In Progress');
  });

  test('prefers Review over In Progress', async ({ page }) => {
    await parkTask(page, projectA, `landing-rv-${Date.now()}`, 'review');

    await openProject(page, 'landing-b');
    await openProject(page, 'landing-a');
    const snap = await boardSnapshot(page);
    expect(snap.center, JSON.stringify(snap)).toBe('Review');
  });

  test('lands on Backlog when only backlog and done tasks exist', async ({ page }) => {
    await parkTask(page, projectB, `landing-bl-${Date.now()}`, 'backlog');
    await parkTask(page, projectB, `landing-dn-${Date.now()}`, 'done');

    // Start on A (Review) and put B's board on Done, so Backlog is not the old position.
    await openProject(page, 'landing-a');
    await openProject(page, 'landing-b');
    await scrollBoardTo(page, 'Done');
    await openProject(page, 'landing-a');
    await openProject(page, 'landing-b');
    const snap = await boardSnapshot(page);
    expect(snap.center, JSON.stringify(snap)).toBe('Backlog');
  });
});
