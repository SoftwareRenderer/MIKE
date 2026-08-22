import { test, expect, type Page } from '@playwright/test';
import { execSync } from 'node:child_process';
import { writeFileSync, rmSync, existsSync } from 'node:fs';
import { join } from 'path';

/**
 * Explorer git History — layout in space-constrained panels.
 *
 * The History panel lives in the resizable left column (default 250px, min
 * 180px; the column's 1px border makes the row width = panel width − 1).
 * Rows degrade in density as the row width shrinks (data-density on
 * .git-graph, see $lib/utils/git-history.ts):
 *
 *   normal  (row >= 245)  full layout — origin text badge, author, date
 *   compact (row >= 205)  origin badge collapses to icon-only
 *   tight   (row >= 180)  date hidden
 *   tighest (row <  180)  author hidden
 *
 * Fields hidden by the density must remain available in the row tooltip.
 *
 * The left column width is controlled by writing to the IndexedDB
 * `explorer-layout` store + reload (deterministic — no drag simulation).
 */

const TEST_REPO = '/tmp/kanban-test-repo';

const UNIQUE_ID = Date.now();
const PROBE_FILE = `narrow-layout-${UNIQUE_ID}.txt`;
const PROBE_SUBJECT = `Narrow layout probe ${UNIQUE_ID}`;
const PROBE_AUTHOR = 'NarrowLayout';
const BARE_ORIGIN = `/tmp/kanban-test-origin-${UNIQUE_ID}`;

let setupOk = false;

test.describe('Explorer history narrow layout', () => {
  test.describe.configure({ mode: 'serial' });

  test.beforeAll(() => {
    if (!existsSync(TEST_REPO)) return;
    try {
      // Local bare remote so the backend reports an origin_tip (origin badge).
      execSync(`git init --bare ${BARE_ORIGIN}`, { stdio: 'pipe' });
      const branch = execSync('git rev-parse --abbrev-ref HEAD', { cwd: TEST_REPO, encoding: 'utf-8' }).trim();
      // Probe commit with a unique author, subject, and a pinned date
      // (2024-05-04) so tooltip assertions are locale-independent.
      writeFileSync(join(TEST_REPO, PROBE_FILE), 'probe for narrow layout test\n');
      execSync(`git add ${PROBE_FILE}`, { cwd: TEST_REPO });
      execSync(`git -c user.name="${PROBE_AUTHOR}" -c user.email="narrow@kanban.local" commit -m "${PROBE_SUBJECT}"`, {
        cwd: TEST_REPO,
        env: { ...process.env, GIT_AUTHOR_DATE: '2024-05-04T14:30:00Z', GIT_COMMITTER_DATE: '2024-05-04T14:30:00Z' },
      });
      execSync(`git remote add origin ${BARE_ORIGIN}`, { cwd: TEST_REPO });
      execSync(`git push origin ${branch}`, { cwd: TEST_REPO, stdio: 'pipe' });
      setupOk = true;
    } catch (e) {
      console.error('[narrow-layout] git setup failed:', e);
    }
  });

  test.afterAll(() => {
    try { execSync('git remote remove origin', { cwd: TEST_REPO, stdio: 'pipe' }); } catch { /* ignore */ }
    rmSync(BARE_ORIGIN, { recursive: true, force: true });
  });

  /** Open the project Explorer and wait for commit rows to render. */
  async function openExplorer(page: Page) {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    await page.locator('.project-item').first().click();
    await page.waitForSelector('.top-bar', { timeout: 10000 });
    await page.locator('.explorer-btn').click();
    await expect(page.locator('.git-commit-row').first()).toBeVisible({ timeout: 15000 });
  }

  /** Persist the left column width via IndexedDB and reload (URL restores the Explorer). */
  async function setExplorerWidth(page: Page, width: number) {
    await page.evaluate((w: number) => new Promise<void>((resolve, reject) => {
      const req = indexedDB.open('kanban-storage', 3);
      req.onsuccess = () => {
        const db = req.result;
        const tx = db.transaction('explorer-layout', 'readwrite');
        tx.objectStore('explorer-layout').put({ leftWidth: w, rightWidth: 180, splitRatio: 0.5 }, 'layout');
        tx.oncomplete = () => { db.close(); resolve(); };
        tx.onerror = () => reject(tx.error);
      };
      req.onerror = () => reject(req.error);
    }), width);
    await page.reload();
    await expect(page.locator('.git-commit-row').first()).toBeVisible({ timeout: 15000 });
  }

  /** The row for the probe commit (unique subject), regardless of parallel commits. */
  function probeRow(page: Page) {
    return page.locator('.git-commit-row', { hasText: PROBE_SUBJECT }).first();
  }

  test('shows all fields at normal width', async ({ page }) => {
    test.skip(!setupOk, 'test repo unavailable');
    test.setTimeout(45000);
    await openExplorer(page);
    // Panel 260px → row 259px → normal
    await setExplorerWidth(page, 260);

    const row = probeRow(page);
    await expect(row).toBeVisible({ timeout: 15000 });
    expect(await page.locator('.git-graph').getAttribute('data-density')).toBe('normal');
    await expect(row.locator('.origin-badge .git-badge-label')).toBeVisible();
    await expect(row.locator('.git-commit-author')).toBeVisible();
    await expect(row.locator('.git-commit-date')).toBeVisible();

    // Normal density: no row tooltip — per-field span titles apply.
    expect(await row.getAttribute('title')).toBeNull();
    await expect(row.locator('.git-commit-subject')).toHaveAttribute('title', PROBE_SUBJECT);
  });

  test('collapses origin badge to icon-only at compact width', async ({ page }) => {
    test.skip(!setupOk, 'test repo unavailable');
    test.setTimeout(45000);
    await openExplorer(page);
    // Panel 220px → row 219px → compact
    await setExplorerWidth(page, 220);

    const row = probeRow(page);
    await expect(row).toBeVisible({ timeout: 15000 });
    expect(await page.locator('.git-graph').getAttribute('data-density')).toBe('compact');
    await expect(row.locator('.origin-badge')).toBeVisible(); // icon stays
    await expect(row.locator('.origin-badge .git-badge-label')).not.toBeVisible(); // text hidden
    await expect(row.locator('.git-commit-author')).toBeVisible(); // author still shown
    await expect(row.locator('.git-commit-date')).toBeVisible(); // date still shown

    // The date must never wrap ("1h ago" → 2 lines breaks the fixed 36px row);
    // the author shrinks/ellipsizes before the date does.
    const date = row.locator('.git-commit-date');
    expect(await date.evaluate((el) => getComputedStyle(el).whiteSpace)).toBe('nowrap');
    expect((await date.boundingBox()).height).toBeLessThan(24); // single line

    // Row tooltip carries the full info (incl. the author/date the badges free up).
    const title = (await row.getAttribute('title')) ?? '';
    expect(title).toContain(PROBE_SUBJECT);
    expect(title).toContain(PROBE_AUTHOR);
  });

  test('hides date and author at minimum width, keeps them in the tooltip', async ({ page }) => {
    test.skip(!setupOk, 'test repo unavailable');
    test.setTimeout(45000);
    await openExplorer(page);
    // Panel 180px (min) → row 179px → tighest
    await setExplorerWidth(page, 180);

    const row = probeRow(page);
    await expect(row).toBeVisible({ timeout: 15000 });
    expect(await page.locator('.git-graph').getAttribute('data-density')).toBe('tighest');
    await expect(row.locator('.origin-badge .git-badge-label')).not.toBeVisible();
    await expect(row.locator('.git-commit-author')).not.toBeVisible();
    await expect(row.locator('.git-commit-date')).not.toBeVisible();
    await expect(row.locator('.git-commit-hash')).toBeVisible();

    // Hidden fields remain available in the row tooltip.
    const title = (await row.getAttribute('title')) ?? '';
    expect(title).toContain(PROBE_SUBJECT);
    expect(title).toContain(PROBE_AUTHOR);
    expect(title).toContain('2024'); // full commit date (pinned to 2024-05-04)
    expect(title).toContain('origin');

    // Subject still gets real space — not crushed to zero by the badges.
    const subjectWidth = await row.locator('.git-commit-subject').evaluate((el) => el.clientWidth);
    expect(subjectWidth).toBeGreaterThan(10);

    // The HEAD row keeps a (truncatable) branch badge.
    await expect(page.locator('.git-commit-row').first().locator('.branch-badge')).toBeVisible();
  });
});
