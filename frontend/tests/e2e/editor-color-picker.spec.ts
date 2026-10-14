import { test, expect, type Browser, type Page } from '@playwright/test';
import { readFileSync, writeFileSync, rmSync, mkdirSync } from 'node:fs';
import { execSync } from 'node:child_process';
import { join } from 'path';
import { csrfFetch } from './helpers';

// Clicking a hex value in a stylesheet opens the color card on that value, and Apply replaces it through
// the editor's own edit path (dirty tab, draft, Save).
//
// Which spellings count as a color is the grammar's answer, measured in
// `src/lib/utils/__tests__/editor-color.unit.ts`; what only a browser can prove is that the underline lands
// on the glyph the click has to hit and that the write reaches the file.
//
// The spec owns its repo and project (/tmp/kanban-test-repo-color) and starts no turn, as in
// editor-wrap-highlight.spec.ts.

const COLOR_REPO = '/tmp/kanban-test-repo-color';
const PROJECT_NAME = 'kanban-test-repo-color';
const FIXTURE_FILE = 'theme.css';
// The html fixture asks the same question at a grammar boundary: the colors sit in a nested CSS parser
// inside `<style>`, and the `style="…"` attribute beside them is not one.
const HTML_FILE = 'page.html';
const HTML_FIXTURE = [
  '<!doctype html>',
  '<style>body { background: #f0f; }</style>',
  '<p style="color: #0f0">not a color token</p>',
  '</html>',
].join('\n');
const FIXTURE = [
  ':root { --brand: #ABC; }',
  '',
  '/* #abcdef was written in prose */',
  '#nav { color: #112233aa; border: 1px solid #abc; }',
  '',
].join('\n');

async function setupPage(browser: Browser) {
  const context = await browser.newContext({ storageState: '/tmp/kanban-test-storage-state.json' });
  const page = await context.newPage();
  await page.goto('/');
  return { context, page };
}

/** The card's own text field — `[role="dialog"]` is never a unique locator. */
const card = (page: Page) => page.locator('.color-card');

test.describe.serial('A clicked color in a stylesheet', () => {
  let projectId = '';

  test.beforeAll(async ({ browser }) => {
    rmSync(COLOR_REPO, { recursive: true, force: true });
    mkdirSync(COLOR_REPO, { recursive: true });
    writeFileSync(join(COLOR_REPO, 'README.md'), 'color-picker fixture repo\n');
    execSync('git init', { cwd: COLOR_REPO, stdio: 'ignore' });
    execSync('git add README.md', { cwd: COLOR_REPO, stdio: 'ignore' });
    execSync('git -c user.email="test@kanban.local" -c user.name="Test" commit -m "Initial commit"', {
      cwd: COLOR_REPO,
      stdio: 'ignore',
    });

    const { context, page } = await setupPage(browser);
    try {
      const res = await csrfFetch(page, '/api/projects', { method: 'POST', body: { path: COLOR_REPO } });
      if (!res.ok) throw new Error(`create color project: ${JSON.stringify(res.data)}`);
      projectId = res.data.id;
    } finally {
      await context.close();
    }
  });

  test.afterAll(async ({ browser }) => {
    const { context, page } = await setupPage(browser);
    try {
      if (projectId) await csrfFetch(page, `/api/projects/${projectId}`, { method: 'DELETE' });
    } finally {
      await context.close();
    }
    rmSync(COLOR_REPO, { recursive: true, force: true });
  });

  test.beforeEach(async ({ page }) => {
    // Each test gets a clean browser context, so an editor draft a test leaves dies with it; the disk is
    // the state that is shared, and it is rewritten here.
    writeFileSync(join(COLOR_REPO, FIXTURE_FILE), FIXTURE);
    writeFileSync(join(COLOR_REPO, HTML_FILE), HTML_FIXTURE);
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    await page.locator('.project-item', { hasText: PROJECT_NAME }).first().click();
    await page.waitForSelector('.top-bar', { timeout: 10000 });
    await page.locator('.explorer-btn').click();
    await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });
    await page.locator('.file-browser-item', { hasText: FIXTURE_FILE }).click();
    await expect(page.locator('.cm-editor')).toBeVisible({ timeout: 15000 });
  });

  /** The text under every underlined token in the open file. */
  async function underlined(page: Page): Promise<string[]> {
    return page.locator('.cm-editor').evaluate((root: HTMLElement) =>
      Array.from(root.querySelectorAll('.cm-color-value')).map((el) => el.textContent ?? ''),
    );
  }

  test('underlines the colors, and nothing the grammar calls a selector or a comment', async ({ page }) => {
    await expect.poll(() => underlined(page), { timeout: 15000 }).toEqual(['#ABC', '#112233aa', '#abc']);
  });

  test('the underline is the color it spells, and the card lands under the value', async ({ page }) => {
    await expect.poll(() => underlined(page), { timeout: 15000 }).toContain('#112233aa');
    const token = page.locator('.cm-color-value', { hasText: '#112233aa' }).first();
    // An underline written in the value's own color is what a reader sees, and only a browser computes one.
    expect(await token.evaluate((el: HTMLElement) => {
      const style = getComputedStyle(el);
      return { color: style.textDecorationColor, line: style.textDecorationLine, cursor: style.cursor };
    })).toEqual({ color: 'rgb(17, 34, 51)', line: 'underline', cursor: 'pointer' });

    await token.click();
    const box = await token.boundingBox();
    const cardBox = await card(page).boundingBox();
    const view = page.viewportSize()!;
    // The card hangs below the token and stays inside the window it opened in.
    expect(cardBox!.y).toBeGreaterThanOrEqual(box!.y + box!.height);
    expect(cardBox!.x).toBeGreaterThanOrEqual(0);
    expect(cardBox!.x + cardBox!.width).toBeLessThanOrEqual(view.width);
    expect(cardBox!.y + cardBox!.height).toBeLessThanOrEqual(view.height);
  });

  test('opens the card on the value the pointer chose, and writes the picked color back in its clothes', async ({
    page,
  }) => {
    // #ABC is uppercase shorthand that cannot hold #1a2b3c, so the write keeps the case and widens.
    await page.locator('.cm-color-value', { hasText: '#ABC' }).first().click();
    await expect(card(page)).toBeVisible();
    await expect(card(page).locator('.token')).toHaveValue('#ABC');
    await expect(card(page).locator('.swatch')).toHaveValue('#aabbcc');

    await card(page).locator('.token').fill('#1a2b3c');
    await card(page).getByRole('button', { name: 'Apply' }).click();

    await expect(card(page)).toHaveCount(0);
    await expect(page.locator('.editor-tab.unsaved')).toHaveCount(1);
    await expect(page.locator('.cm-editor')).toContainText('#1A2B3C');

    await page.locator('.editor-save-btn').click();
    await expect(page.locator('.editor-tab.unsaved')).toHaveCount(0);
    await expect
      .poll(() => readFileSync(join(COLOR_REPO, FIXTURE_FILE), 'utf8'))
      .toContain('--brand: #1A2B3C;');
  });

  test('a tap opens it too, and Escape leaves the file alone', async ({ browser }) => {
    const context = await browser.newContext({ hasTouch: true, storageState: '/tmp/kanban-test-storage-state.json' });
    const page = await context.newPage();
    try {
      await page.goto('/');
      await page.waitForSelector('.project-item', { timeout: 10000 });
      await page.locator('.project-item', { hasText: PROJECT_NAME }).first().click();
      await page.waitForSelector('.top-bar', { timeout: 10000 });
      await page.locator('.explorer-btn').click();
      await page.locator('.file-browser-item', { hasText: FIXTURE_FILE }).click();
      await expect(page.locator('.cm-editor')).toBeVisible({ timeout: 15000 });
      await expect.poll(() => underlined(page), { timeout: 15000 }).toContain('#112233aa');

      const token = page.locator('.cm-color-value', { hasText: '#112233aa' }).first();
      const box = await token.boundingBox();
      await page.touchscreen.tap(box!.x + box!.width / 2, box!.y + box!.height / 2);
      await expect(card(page)).toBeVisible();
      // The alpha the file wrote is the field's own text, so the card shows the token as the file spells it.
      await expect(card(page).locator('.token')).toHaveValue('#112233aa');

      await page.keyboard.press('Escape');
      await expect(card(page)).toHaveCount(0);
      await expect(page.locator('.editor-tab.unsaved')).toHaveCount(0);
    } finally {
      await context.close();
    }
  });

  test('a style block inside an html document is a stylesheet where the colors are', async ({ page }) => {
    await page.locator('.file-browser-item', { hasText: HTML_FILE }).click();
    await expect(page.locator('.cm-editor')).toBeVisible({ timeout: 15000 });
    await expect.poll(() => underlined(page), { timeout: 15000 }).toEqual(['#f0f']);

    await page.locator('.cm-color-value', { hasText: '#f0f' }).first().click();
    await card(page).locator('.token').fill('#3fb950');
    await card(page).getByRole('button', { name: 'Apply' }).click();
    await expect(page.locator('.cm-editor')).toContainText('background: #3fb950');
  });

  test('a click on text the grammar is not claiming opens nothing', async ({ page }) => {
    const selector = page.locator('.cm-line', { hasText: '#nav' }).first();
    await expect(selector).toBeVisible();
    await selector.click();
    await expect(card(page)).toHaveCount(0);

    const comment = page.locator('.cm-line', { hasText: '#abcdef' }).first();
    await comment.click();
    await expect(card(page)).toHaveCount(0);
  });
});
