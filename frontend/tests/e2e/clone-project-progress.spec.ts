import { test, expect, type Browser, type Page } from '@playwright/test';
import { rmSync, mkdirSync, writeFileSync } from 'node:fs';
import { execSync } from 'node:child_process';
import { join } from 'path';
import { csrfFetch } from './helpers';

// "Clone from Git" shows a determinate progress bar in the projects sidebar
// right after the user clicks Clone — the clone streams live percentages
// from POST /api/projects/clone (SSE), and the sidebar row is an optimistic
// placeholder until the server finishes and returns the real project.
//
// Isolation: dedicated source repo (/tmp/kanban-test-clone-src-<ts>) and a
// dedicated target dir under /tmp/kanban-test-data (wiped by global-setup
// every run) — never touches the shared kanban-test-repo.

const TS = Date.now();
const SRC_REPO = `/tmp/kanban-test-clone-src-${TS}`;
const TARGET_DIR = `/tmp/kanban-test-data/clone-target-${TS}`;
const REPO_NAME = `kanban-test-clone-src-${TS}`; // basename of SRC_REPO
const CLONED_PATH = join(TARGET_DIR, REPO_NAME);

// `page` fixtures aren't available in beforeAll/afterAll — create a dedicated
// context (with the harness's logged-in storage state) for setup/teardown.
async function setupPage(browser: Browser) {
  const context = await browser.newContext({
    storageState: '/tmp/kanban-test-storage-state.json',
  });
  const page = await context.newPage();
  await page.goto('/');
  return { context, page };
}

/** Open Add Project, navigate the file browser to dir, fill the clone form. */
async function openCloneForm(page: Page, dir: string, url: string) {
  await page.click('.add-project-btn');
  await expect(page.locator('.modal')).toBeVisible();

  const pathInput = page.locator('.path-input');
  await pathInput.fill(dir);
  await pathInput.press('Enter');
  await expect(page.locator('.current-path')).toContainText(dir);

  await page.click('button.tab:has-text("Clone from Git")');
  await page.fill('#clone-url', url);
}

test.describe.serial('Clone from Git — sidebar progress bar', () => {
  let projectId = '';

  test.beforeAll(async () => {
    // Dedicated source repo with a real commit (never the shared test repo).
    mkdirSync(SRC_REPO, { recursive: true });
    writeFileSync(join(SRC_REPO, 'README.md'), 'clone fixture repo\n');
    execSync('git init', { cwd: SRC_REPO, stdio: 'ignore' });
    execSync('git add README.md', { cwd: SRC_REPO, stdio: 'ignore' });
    execSync('git -c user.email="test@kanban.local" -c user.name="Test" commit -m "Initial commit"', {
      cwd: SRC_REPO,
      stdio: 'ignore',
    });
    // Dedicated clone target, under the test data dir (wiped each run).
    mkdirSync(TARGET_DIR, { recursive: true });
  });

  test.afterAll(async ({ browser }) => {
    const { context, page } = await setupPage(browser);
    try {
      if (projectId) {
        await csrfFetch(page, `/api/projects/${projectId}`, { method: 'DELETE' });
      }
    } finally {
      await context.close();
    }
    rmSync(SRC_REPO, { recursive: true, force: true });
    rmSync(TARGET_DIR, { recursive: true, force: true });
  });

  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item');
  });

  test('shows a progress bar in the sidebar right after clicking Clone', async ({ page }) => {
    await openCloneForm(page, TARGET_DIR, SRC_REPO);
    await page.locator('button.btn-primary:has-text("Clone")').click();

    // Immediately after the click: the modal is closed and the placeholder
    // row with its progress bar is visible in the sidebar (the real project
    // only exists on the server once the clone finishes).
    await expect(page.locator('.modal')).toHaveCount(0);
    const cloningRow = page.locator('.project-item:has(.clone-progress)');
    await expect(cloningRow).toBeVisible();
    await expect(cloningRow.locator('.project-name')).toHaveText(REPO_NAME);
    // The bar is a real (determinate) progressbar, not a spinner.
    await expect(cloningRow.locator('.clone-progress')).toHaveAttribute('role', 'progressbar');
    // The cloning row is not selectable and has no remove button.
    await expect(cloningRow).toHaveAttribute('aria-busy', 'true');
    await expect(
      page.locator('.project-item-wrapper:has(.clone-progress)').locator('.project-remove-btn')
    ).toHaveCount(0);

    // Clone completes: the bar is gone and the new project is selected.
    await expect(page.locator('.clone-progress')).toHaveCount(0);
    await expect(page.locator('.project-item-wrapper.active .project-name')).toHaveText(REPO_NAME);

    // The project exists on the server at the cloned path.
    projectId = await page.evaluate(async (wantPath) => {
      const res = await fetch('/api/projects');
      const projects = await res.json();
      const p = projects.find((x: any) => x.path === wantPath);
      return p?.id ?? '';
    }, CLONED_PATH);
    expect(projectId).toBeTruthy();
  });

  test('failed clone removes the placeholder and shows the error', async ({ page }) => {
    // Clone the same repo into the same target again — the cloned directory
    // already exists, so the server rejects it.
    await openCloneForm(page, TARGET_DIR, SRC_REPO);
    await page.locator('button.btn-primary:has-text("Clone")').click();

    // The placeholder may not outlive the fast local clone — but it must
    // never stick around, and the error toast must explain the failure.
    await expect(page.locator('.clone-progress')).toHaveCount(0);
    await expect(page.locator('.error-toast')).toContainText('already exists');

    // Exactly one project at the cloned path (the one from the first test).
    const count = await page.evaluate(async (wantPath) => {
      const res = await fetch('/api/projects');
      const projects = await res.json();
      return projects.filter((x: any) => x.path === wantPath).length;
    }, CLONED_PATH);
    expect(count).toBe(1);
  });
});
