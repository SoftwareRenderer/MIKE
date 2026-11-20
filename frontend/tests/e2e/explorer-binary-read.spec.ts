import { test, expect, type Page } from '@playwright/test';
import { rmSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import {
  getProjectId, gitIn, gotoProject, isolatedProject, openChangesPanel, ownProjectId, ownProjectPath, selectProject,
} from './helpers';

// ============================================================
// A file whose bytes are not text, opened from the Explorer
// ============================================================
//
// The wire between the Go refusal and the browser's picture, which neither unit test reaches: a real PNG in
// a real checkout, refused as text by the server and painted from the raw route. The halves are in
// `backend/server/handler_fs_read_text_test.go` and `__tests__/file-editor-binary.unit.svelte.ts`. A console
// error fails this spec even when the picture decodes — an empty JSON body reached the browser as one.
//
// Writes and commits in its own project (isolatedProject), both undone in afterAll. It starts no turn and
// reads no config.

isolatedProject();

/** A 1×1 opaque PNG. A file no decoder accepts would prove the refusal, not the picture. */
const PNG = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==',
  'base64',
);
/** A second valid PNG, so "changed" is a real change and not the same bytes twice. */
const PNG_B = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAAC0lEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==',
  'base64',
);

/**
 * What the browser said was wrong while the reader was looking. Two lines are the point of the run rather
 * than a failure: the 415 the server was asked to answer, and the lane's self-signed CA refusing one script
 * fetch. Anything else — above all `Unexpected end of JSON input` — means the bytes crossed wrong.
 */
function consoleErrors(page: Page): string[] {
  const seen: string[] = [];
  page.on('console', (msg) => {
    if (msg.type() !== 'error') return;
    const text = msg.text();
    if (/415 \(Unsupported Media Type\)/.test(text)) return;
    if (/SSL certificate/i.test(text)) return;
    seen.push(text);
  });
  return seen;
}

function projectDir(): string {
  const dir = ownProjectPath();
  if (!dir) throw new Error('this spec must call isolatedProject() to own a project directory');
  return dir;
}

function write(rel: string, bytes: Buffer): void {
  writeFileSync(join(projectDir(), rel), bytes);
}

/** Decoded by the browser, not merely attached: a broken `<img>` is a pane with nothing in it. */
async function expectDecoded(locator: ReturnType<Page['locator']>) {
  await expect(locator).toBeVisible({ timeout: 10000 });
  await expect
    .poll(() => locator.evaluate((el: HTMLImageElement) => el.naturalWidth), { timeout: 10000 })
    .toBeGreaterThan(0);
}

const IMAGE = 'pixel.png';
const CHANGED = 'changed.png';
const BINARY = 'weights.dat';
const written: string[] = [];

test.describe('a non-text file in the Explorer', () => {
  test.describe.configure({ mode: 'serial' });

  let committed = false;
  const undo = (command: string) => {
    try { gitIn(projectDir(), command); } catch { /* the tree was already put back */ }
  };

  test.afterAll(() => {
    if (committed) undo('reset --hard HEAD~1 -q');
    for (const rel of written) rmSync(join(projectDir(), rel), { force: true });
  });

  async function openFile(page: Page, rel: string): Promise<void> {
    const projectId = await getProjectId(page);
    await selectProject(page, projectId);
    await expect(page.locator('.top-bar')).toBeVisible({ timeout: 10000 });
    await page.locator('.explorer-btn').click();
    const row = page.locator('.file-browser-item', { hasText: rel.split('/').pop()! });
    await expect(row).toBeVisible({ timeout: 10000 });
    await row.click();
  }

  test('a picture is painted from the raw route', async ({ page }) => {
    written.push(IMAGE);
    write(IMAGE, PNG);
    await gotoProject(page);
    const errors = consoleErrors(page);
    await openFile(page, IMAGE);

    await expectDecoded(page.locator('.editor-content img.editor-image-img'));
    expect(errors).toEqual([]);
  });

  test('a binary file that is not a picture says so', async ({ page }) => {
    written.push(BINARY);
    write(BINARY, Buffer.concat([PNG, Buffer.from([0, 1, 2, 3])]));
    await gotoProject(page);
    const errors = consoleErrors(page);
    await openFile(page, BINARY);

    await expect(page.locator('.editor-content .editor-placeholder')).toContainText('Binary file', { timeout: 10000 });
    await expect(page.locator('.editor-content .cm-editor')).toHaveCount(0);
    expect(errors).toEqual([]);
  });

  test('the Changes panel reads both sides of a changed image', async ({ page }) => {
    written.push(CHANGED);
    write(CHANGED, PNG);
    gitIn(projectDir(), `add -- ${CHANGED}`);
    gitIn(projectDir(), "-c user.email=e2e@e2e.local -c user.name=e2e commit -q -m 'the image to compare'");
    committed = true;

    write(CHANGED, PNG_B);
    await gotoProject(page);
    const errors = consoleErrors(page);
    await openChangesPanel(page, ownProjectId());

    const row = page.locator('.diff-file-section').filter({
        has: page.locator('.diff-file-path', { hasText: CHANGED }),
    });
    await expect(row).toBeVisible({ timeout: 10000 });
    const button = row.locator('.diff-binary-notice button', { hasText: /view image/i });
    await expect(button).toBeVisible({ timeout: 10000 });
    await button.click();

    const panes = page.locator('[role="dialog"] img.image-diff-img');
    await expectDecoded(panes.nth(0));
    await expectDecoded(panes.nth(1));
    expect(errors).toEqual([]);
  });
});
