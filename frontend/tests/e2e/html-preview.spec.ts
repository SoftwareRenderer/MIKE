import { test, expect, type Browser, type Page, type Frame } from '@playwright/test';
import { mkdirSync, writeFileSync, rmSync } from 'node:fs';
import { join } from 'path';

// HTML preview extension — end-to-end: installs the local extension via
// install-local, then verifies the Preview button (HTML-family only), the
// rendered preview (scripts run, root-relative + relative sibling assets
// load), live refresh on edits, code-view restore, and host isolation
// (the previewed page probes its own sandbox: no host DOM/storage/cookies,
// host API responses CORS-rejected). Fixtures live in a uniquely named
// /tmp/kanban-test-repo subdir removed in afterAll.

const TEST_REPO = '/tmp/kanban-test-repo';
const TS = Date.now();
const FIXTURE_DIR = `${TEST_REPO}/html-preview-${TS}`;
const PAGE_FILE = 'index.html';
const CSS_FILE = 'style.css';
const REL_CSS_FILE = 'rel.css';
const SVG_FILE = 'logo.svg';

// style.css is referenced root-relative (rewritten by the extension);
// rel.css relatively — both paths covered.
const HTML_FIXTURE = `<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8">
  <link rel="stylesheet" href="/${CSS_FILE}">
  <link rel="stylesheet" href="${REL_CSS_FILE}">
</head>
<body>
  <h1 id="title">HTML Preview Fixture</h1>
  <p id="js-target">before</p>
  <div id="styled">styled box</div>
  <div id="rel-box">relative box</div>
  <img id="data-img" alt="dot" src="data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='12' height='12'%3E%3Crect width='12' height='12' fill='limegreen'/%3E%3C/svg%3E">
  <div id="probes"></div>
  <script>document.getElementById('js-target').textContent = 'JS-RAN-42';</script>
  <script>
    // Isolation probes: run from inside the previewed page (opaque origin,
    // sandboxed without allow-same-origin) and report what the browser lets
    // it do. All host-side access must be blocked.
    (function () {
      const r = {};
      const done = () => { document.getElementById('probes').textContent = JSON.stringify(r); };
      try { void window.top.document.title; r.topDom = 'read'; }
      catch (e) { r.topDom = 'blocked'; }
      try { void window.localStorage; r.storage = 'available'; }
      catch (e) { r.storage = 'blocked'; }
      try { void document.cookie; r.cookie = 'readable'; }
      catch (e) { r.cookie = 'blocked'; }
      fetch('/api/projects').then(
        () => { r.api = 'readable'; done(); },
        () => { r.api = 'rejected'; done(); }
      );
    })();
  </script>
</body>
</html>
`;

const CSS_FIXTURE = `#styled {
  color: rgb(255, 0, 85);
  background-color: rgb(10, 20, 30);
  padding: 4px;
}
`;

const REL_CSS_FIXTURE = `#rel-box {
  color: rgb(0, 170, 170);
}
`;

const SVG_FIXTURE = `<svg xmlns="http://www.w3.org/2000/svg" width="40" height="40"><rect width="40" height="40" fill="teal"/></svg>`;

// No page fixture in beforeAll/afterAll — dedicated logged-in context.
async function setupPage(browser: Browser) {
  const context = await browser.newContext({
    storageState: '/tmp/kanban-test-storage-state.json',
  });
  const page = await context.newPage();
  await page.goto('/');
  return { context, page };
}

async function installLocal(page: Page): Promise<void> {
  // Local author = on-disk namespace dir (slugified); resolve like the Settings UI.
  const res = await page.evaluate(async () => {
    const session = await (await fetch('/api/auth/session')).json();
    const csrf = session.csrf_token || '';
    const localRes = await fetch('/api/extensions/local');
    const local = await localRes.json();
    const entry = local.find((e: any) => e.name === 'html-preview');
    if (!entry) return { status: 404, text: 'html-preview not in local extensions list' };
    const r = await fetch('/api/extensions/install-local', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf },
      credentials: 'include',
      body: JSON.stringify({ author: entry.author, name: entry.name }),
    });
    const text = await r.text(); // SSE stream; fetch resolves when install completes
    return { status: r.status, text };
  });
  if (res.status !== 200 && res.status !== 201) {
    throw new Error(`install-local failed (${res.status}): ${res.text.slice(0, 300)}`);
  }
}

async function getHtmlPreviewExtId(page: Page): Promise<string> {
  return await page.evaluate(async () => {
    const res = await fetch('/api/extensions');
    const exts = await res.json();
    return exts.find((e: any) => e.name === 'html-preview')?.id || '';
  });
}

/** Open the project explorer and open `file` (relative to repo root, with subfolders). */
async function openFile(page: Page, parts: string[]): Promise<void> {
  await page.goto('/');
  await page.waitForSelector('.card', { timeout: 10000 });
  await page.locator('.project-item').first().click();
  await page.waitForSelector('.top-bar', { timeout: 10000 });
  await page.locator('.explorer-btn').click();
  await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });
  await expect(page.locator('.file-browser-list')).toBeVisible({ timeout: 10000 });
  for (const part of parts) {
    await page.locator('.file-browser-item', { hasText: part }).first().click();
    await page.waitForTimeout(150); // tree expands / editor loads
  }
  await expect(page.locator('.file-editor.active .cm-editor')).toBeVisible({ timeout: 10000 });
}

function previewButton(page: Page) {
  return page.locator('.file-editor.active .ext-footer-btn', { hasText: 'Preview' });
}

/** The nested preview frame (loaded from /api/preview/<token>/). */
async function previewFrameOf(page: Page): Promise<Frame> {
  for (let i = 0; i < 50; i++) {
    const f = page.frames().find(fr => fr.url().includes('/api/preview/'));
    if (f) return f;
    await page.waitForTimeout(200);
  }
  throw new Error('preview frame did not appear');
}

test.describe.serial('HTML Preview extension', () => {
  let extId = '';

  test.beforeAll(async ({ browser }) => {
    mkdirSync(FIXTURE_DIR, { recursive: true });
    writeFileSync(join(FIXTURE_DIR, PAGE_FILE), HTML_FIXTURE);
    writeFileSync(join(FIXTURE_DIR, CSS_FILE), CSS_FIXTURE);
    writeFileSync(join(FIXTURE_DIR, REL_CSS_FILE), REL_CSS_FIXTURE);
    writeFileSync(join(FIXTURE_DIR, SVG_FILE), SVG_FIXTURE);

    const { context, page } = await setupPage(browser);
    try {
      await installLocal(page);
      await expect.poll(() => getHtmlPreviewExtId(page), { timeout: 15000 }).not.toBe('');
      extId = await getHtmlPreviewExtId(page);
    } finally {
      await context.close();
    }
  });

  test.afterAll(async ({ browser }) => {
    try {
      if (extId) {
        const { context, page } = await setupPage(browser);
        try {
          await page.evaluate(async (id) => {
            const session = await (await fetch('/api/auth/session')).json();
            await fetch(`/api/extensions/${id}`, {
              method: 'DELETE',
              headers: { 'X-CSRF-Token': session.csrf_token || '' },
              credentials: 'include',
            });
          }, extId);
        } finally {
          await context.close();
        }
      }
    } catch { /* best-effort cleanup */ }
    try { rmSync(FIXTURE_DIR, { recursive: true, force: true }); } catch { /* ignore */ }
  });

  test('Preview button appears for HTML/SVG files but not Markdown', async ({ page }) => {
    // .html → button present
    await openFile(page, [`html-preview-${TS}`, PAGE_FILE]);
    await expect(previewButton(page)).toBeVisible({ timeout: 10000 });

    // .md → no button
    await openFile(page, ['README.md']);
    await expect(previewButton(page)).toHaveCount(0);

    // .svg → button present
    await openFile(page, [`html-preview-${TS}`, SVG_FILE]);
    await expect(previewButton(page)).toBeVisible({ timeout: 10000 });
  });

  test('clicking Preview renders the file: scripts run and sibling assets load', async ({ page }) => {
    await openFile(page, [`html-preview-${TS}`, PAGE_FILE]);
    const btn = previewButton(page);
    await expect(btn).toBeVisible({ timeout: 10000 });

    await btn.click();

    // Code view hidden, overlay shown.
    await expect(page.locator('.file-editor.active .editor-wrapper')).toHaveCSS('display', 'none', { timeout: 10000 });
    await expect(btn).toHaveClass(/active/);

    const frame = await previewFrameOf(page);

    // Inline <script> executed in the preview document.
    await expect(frame.locator('#js-target')).toHaveText('JS-RAN-42', { timeout: 15000 });

    // Root-relative /style.css — rewritten to file-relative by the extension.
    await expect(frame.locator('#styled')).toHaveCSS('color', 'rgb(255, 0, 85)', { timeout: 15000 });
    await expect(frame.locator('#styled')).toHaveCSS('background-color', 'rgb(10, 20, 30)');

    // Relative rel.css also loads.
    await expect(frame.locator('#rel-box')).toHaveCSS('color', 'rgb(0, 170, 170)');

    // data: image renders.
    await expect(frame.locator('#data-img')).toBeVisible();

    // Toggle off → code view restored.
    await btn.click();
    await expect(page.locator('.file-editor.active .editor-wrapper')).toBeVisible({ timeout: 10000 });
    await expect(btn).not.toHaveClass(/active/);
  });

  test('code-view edits refresh the preview live', async ({ page }) => {
    await openFile(page, [`html-preview-${TS}`, PAGE_FILE]);
    const btn = previewButton(page);
    await expect(btn).toBeVisible({ timeout: 10000 });
    await btn.click();
    await expect(page.locator('.file-editor.active .editor-wrapper')).toHaveCSS('display', 'none', { timeout: 10000 });
    const frame = await previewFrameOf(page);
    await expect(frame.locator('#title')).toHaveText('HTML Preview Fixture', { timeout: 15000 });

    // Edit the code view (the CodeMirror view stays in the DOM while hidden).
    const updated = `LIVE-UPDATED-${TS}`;
    const newContent = HTML_FIXTURE.replace('HTML Preview Fixture', updated);
    await page.evaluate((content) => {
      const cm = document.querySelector('.file-editor.active .cm-editor') as any;
      cm.__view.dispatch({
        changes: { from: 0, to: cm.__view.state.doc.length, insert: content },
      });
    }, newContent);

    // Debounced re-serve + reload → the preview shows the new title.
    await expect.poll(async () => {
      const f = page.frames().find(fr => fr.url().includes('/api/preview/'));
      if (!f) return null;
      const el = f.locator('#title');
      return (await el.count()) > 0 ? await el.textContent() : null;
    }, { timeout: 15000 }).toContain(updated);

    // Toggle off and restore the original fixture (cleanup for other tests).
    await btn.click();
    await expect(page.locator('.file-editor.active .editor-wrapper')).toBeVisible({ timeout: 10000 });
  });

  test('previewed page is isolated from the host', async ({ page }) => {
    await openFile(page, [`html-preview-${TS}`, PAGE_FILE]);
    const btn = previewButton(page);
    await expect(btn).toBeVisible({ timeout: 10000 });
    await btn.click();

    // The nested iframe's sandbox attribute is the whole isolation model:
    // scripts run, but without allow-same-origin the page gets an opaque
    // origin. Assert it as deployed, then verify the consequences from
    // inside the previewed document itself (its own probes).
    const extFrame = page.frames().find(fr => fr.url().includes('/extensions/') && fr.url().includes('html-preview'));
    expect(extFrame, 'extension frame present').toBeTruthy();
    const sandbox = await extFrame!.locator('#preview-frame').getAttribute('sandbox');
    expect(sandbox, 'nested preview iframe sandbox attribute').toBe('allow-scripts');

    const frame = await previewFrameOf(page);
    await expect(frame.locator('#probes')).not.toHaveText('', { timeout: 15000 });
    const probes = JSON.parse((await frame.locator('#probes').textContent())!);
    expect(probes.topDom, 'host DOM unreadable').toBe('blocked');
    expect(probes.storage, 'Web Storage unavailable').toBe('blocked');
    expect(probes.cookie, 'host cookies (session/CSRF) unreadable').toBe('blocked');
    // The server sets no CORS headers: cross-origin (opaque-origin) reads of
    // any host API response are rejected, so the page can't exfiltrate data.
    expect(probes.api, 'host API responses unreadable (CORS)').toBe('rejected');

    await btn.click();
    await expect(page.locator('.file-editor.active .editor-wrapper')).toBeVisible({ timeout: 10000 });
  });
});
