import { test, expect, type Browser, type BrowserContext, type Page, type Frame } from '@playwright/test';
import { mkdirSync, rmSync, rmdirSync, writeFileSync } from 'node:fs';
import { createServer, type Server as HttpServer } from 'node:http';
import { execSync } from 'node:child_process';
import { dirname, join, resolve } from 'path';
import { fileURLToPath } from 'url';
import { csrfFetch } from './helpers';

// Extension-frame channel security — end-to-end.
//
// Threat (the reason for the handshake in src/lib/extensions/channel.ts): a
// sandboxed extension iframe is an opaque origin, and the sandbox flags belong
// to the browsing CONTEXT, so any document that ends up in that frame is *still*
// an opaque origin in the same window. `ev.origin === 'null'` + `ev.source`
// therefore prove "that frame", not "the document we mounted". Before the fix, a
// different document in that frame could (a) receive everything the host pushes
// with targetOrigin '*' — `init` (extension config, grants, context) and
// `editor.contentChange` (the open file, on every keystroke) — and (b) invoke
// the extension's granted capabilities itself.
//
// Two escape routes, two tests:
//
//   1. SAME-ORIGIN (the route no CSP can stop): the frame navigates to a sibling
//      file inside the extension's own directory. Same origin, same CSP, nothing
//      blocks it — it simply is not the document the host mounted, and it never
//      saw the `?extch=` token that lives in the mounted document's URL. It must
//      get no `init`, no pushed editor content, and no answer to a forged
//      handshake or a forged capability call.
//   2. CROSS-ORIGIN: the frame navigates itself to an attacker origin. The host
//      CSP's `frame-src 'self'` (backend/server/middleware.go) blocks that in
//      flight — the hostile origin is never contacted — while the load watchdog
//      still notices and disarms. Pinned here so both layers stay in place.
//
// And the counter-assertion that keeps the watchdog honest: an extension that
// owns NESTED iframes (html-preview renders the previewed document in one) may
// navigate them as much as it likes. A nested frame's `load` belongs to the
// nested browsing context — it must never count as a reload of the document the
// host mounted, or every preview refresh would disarm the channel it depends on.
// The nested-iframe test navigates three ways and requires the channel to stay armed
// afterwards.
//
// The last test checks the other side of the sandbox boundary: a document opened outside
// a host frame is sandboxed by its own response.
//
// Fixture notes: an extension document is served with the host CSP
// (`script-src 'self' 'nonce-…'`), so EVERY script in the fixture is an external
// file — inline scripts are blocked (exactly like the shipped extensions, which
// load /host-client.js plus their own .js). The entry document also lands on its
// DIRECTORY url (Go's FileServer 301s `/index.html` → `./`), so the mounted frame
// is found by the `?extch=` token read off the host's own iframe, not by filename.
//
// Isolation: installs a fixture UI extension from <repo>/extensions/<author>/
// <name> and opens a file in a DEDICATED repo/project (so nothing races other
// specs' file editors). Serialized `isolated` project — installing an extension
// is server state. Fixture dir, project and extension removed in afterAll.

const TS = Date.now();
const AUTHOR = 'extchan-fixture';
// Lowercase + hyphens only: the extension name goes into a URL path verbatim
// (the host percent-encodes it), and this spec matches frames on that path.
const EXT_NAME = `chan-test-${TS}`;
const REPO = '/tmp/kanban-test-repo-extchan';
const PROJECT_NAME = 'kanban-test-repo-extchan';
const DOC_FILE = 'notes.txt';
/** A line of the open file — must never reach a document other than the one mounted. */
const SECRET = `SECRET-FILE-${TS}`;
/** The extension's config value — carried in `init`, same rule. */
const SECRET_CONFIG = `cfg-secret-${TS}`;
/** Proves the frame really was armed before it escaped (a host-handled log.write). */
const PRE_ESCAPE_TAG = `pre-escape-${TS}`;
/** Logged once the nested-iframe navigations have all completed. */
const NESTED_DONE_TAG = `nested-done-${TS}`;

/** Where the Go server scans for installable local extensions. */
const HERE = dirname(fileURLToPath(import.meta.url));
const EXTENSIONS_DIR = process.env.MIKE_EXTENSIONS_DIR
  || resolve(HERE, '..', '..', '..', 'extensions');
const EXT_DIR_PATH = join(EXTENSIONS_DIR, AUTHOR, EXT_NAME);

/** Same-origin sibling document — the unmounted document that takes over the frame. */
const SIBLING_URL = `/extensions/${AUTHOR}/${EXT_NAME}/other.html`;

// The hostile origin for the cross-origin route. It must never receive a request.
let payloadServer: HttpServer;
let payloadBase = '';
let payloadHits = 0;

// ── fixture extension ────────────────────────────────────────────────
const MANIFEST = JSON.stringify({
  display_name: 'Channel Fixture',
  description: 'Test fixture: exercises the extension-frame channel handshake.',
  author: AUTHOR,
  version: '0.0.1',
  priority: 0,
  capabilities: {
    ui: {
      injection_points: ['fileEditor'],
      // Mirrors markdown-preview: reading the file also implies the host pushes
      // subsequent edits (editor.contentChange) into the frame.
      permissions: ['editor.addFooterButton', 'editor.getContent'],
    },
  },
}, null, 4);

/** `?v=` keeps a rewritten main.js from being served out of any cache: each test
 * rewrites it for its own mode while the extension directory (the URL path)
 * stays the same. */
function indexHtml(mode: string): string {
  return `<!doctype html><html><body>
<div id="log"></div>
<script src="/host-client.js"></` + `script>
<script src="main.js?v=${TS}-${mode}"></` + `script>
</body></html>`;
}

/** A page that records every message its frame receives and forges a takeover. */
const HIJACK_SCRIPT = `
var el = document.getElementById('log');
window.addEventListener('message', function (e) {
  el.textContent += '|MSG ' + JSON.stringify(e.data);
});
// Pretend to be the bridge: guess the mount token, then read the open file.
setTimeout(function () {
  window.parent.postMessage({ type: 'exthello', token: 'guessed' }, '*');
  window.parent.postMessage({ type: 'call', id: 'evil-1', cap: 'editor.getContent', args: {} }, '*');
}, 300);
`;

/**
 * The document the host mounts. Logs every inbound message (so the test can see
 * exactly what the host delivered), proves the channel is armed, and in the
 * escape modes navigates the frame away from itself once that is demonstrable.
 */
function mainJs(mode: 'stay' | 'sibling' | 'cross-origin' | 'nested'): string {
  const target =
    mode === 'sibling' ? JSON.stringify(SIBLING_URL)
    : mode === 'cross-origin' ? JSON.stringify(payloadBase + '/evil.html')
    : '';
  return `
var el = document.getElementById('log');
function log(s) { el.textContent += s + '\\n'; }
window.addEventListener('message', function (e) { log('MSG ' + JSON.stringify(e.data)); });
window.extHost.ready().then(function () {
  log('ARMED granted=' + window.extHost.granted.slice().sort().join(','));
  log('CONTEXT ' + JSON.stringify(window.extHost.context));
  return window.extHost.getConfig().then(function (c) {
    log('CONFIG ' + JSON.stringify(c));
  }, function (err) { log('ERR ' + err.message); });
}).then(function () {
  // A host-handled capability call: the host logs this line under the
  // extension's name, proving to the test that the channel was genuinely armed.
  // The escape below happens only after it has round-tripped.
  return window.extHost.log('info', ${JSON.stringify(PRE_ESCAPE_TAG)});
})${
    mode === 'nested'
      // A sandboxed child frame (like html-preview's preview frame), navigated
      // three times — setAttribute('src') twice after the initial mount.
      ? `.then(function () {
  var f = document.createElement('iframe');
  f.setAttribute('sandbox', 'allow-scripts');
  f.setAttribute('src', 'inner-a.html');
  document.body.appendChild(f);
  setTimeout(function () { f.setAttribute('src', 'inner-b.html'); }, 200);
  setTimeout(function () { f.setAttribute('src', 'inner-a.html'); }, 400);
  setTimeout(function () { log(${JSON.stringify(NESTED_DONE_TAG)}); }, 800);
})`
      : target
    ? `.then(function () {
  setTimeout(function () { window.location.href = ${target}; }, 300);
})`
    : ''}.catch(function (err) { log('CHAIN_ERR ' + err.message); });
`;
}

function writeFixtureExtension(mode: 'stay' | 'sibling' | 'cross-origin' | 'nested'): void {

  mkdirSync(EXT_DIR_PATH, { recursive: true });
  writeFileSync(join(EXT_DIR_PATH, 'manifest.json'), MANIFEST);
  writeFileSync(join(EXT_DIR_PATH, 'index.html'), indexHtml(mode));
  writeFileSync(join(EXT_DIR_PATH, 'main.js'), mainJs(mode));
  // The same-origin sibling that can take the frame over.
  writeFileSync(join(EXT_DIR_PATH, 'other.html'), `<!doctype html><html><body>
<div id="log">AT-SIBLING</div>
<script src="other.js?v=${TS}"></` + `script>
</body></html>`);
  writeFileSync(join(EXT_DIR_PATH, 'other.js'), HIJACK_SCRIPT);
  // Pages for the nested-frame mode: the extension's OWN child frames, navigated
  // repeatedly. They carry no logic — only their loads matter.
  const innerPage = (label: string) => `<!doctype html><html><head>
<title>${label}</title></head><body>${label}</body></html>`;
  writeFileSync(join(EXT_DIR_PATH, 'inner-a.html'), innerPage('inner-a'));
  writeFileSync(join(EXT_DIR_PATH, 'inner-b.html'), innerPage('inner-b'));
}

// ── helpers ──────────────────────────────────────────────────────────
/** `browser.newContext()` does not reliably inherit the project's baseURL. */
const BASE_URL = 'https://localhost:3001';
const STORAGE_STATE = '/tmp/kanban-test-storage-state.json.isolated';

async function setupPage(browser: Browser): Promise<{ context: BrowserContext; page: Page }> {
  const context = await browser.newContext({ storageState: STORAGE_STATE, baseURL: BASE_URL });
  const page = await context.newPage();
  await page.goto('/');
  return { context, page };
}

/** install-local the fixture (SSE response — the fetch resolves when it ends). */
async function installFixture(page: Page): Promise<void> {
  const res = await page.evaluate(async ({ author, name }) => {
    const session = await (await fetch('/api/auth/session')).json();
    const local = await (await fetch('/api/extensions/local')).json();
    const entry = (local as any[]).find((e) => e.name === name);
    if (!entry) return { status: 404, text: 'fixture not in local extensions list' };
    const r = await fetch('/api/extensions/install-local', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': session.csrf_token || '' },
      credentials: 'include',
      body: JSON.stringify({ author, name }),
    });
    return { status: r.status, text: await r.text() };
  }, { author: AUTHOR, name: EXT_NAME });
  if (res.status !== 200 && res.status !== 201) {
    throw new Error(`install-local failed (${res.status}): ${res.text.slice(0, 300)}`);
  }
}

async function getFixtureExtId(page: Page): Promise<string> {
  return await page.evaluate(async (name) => {
    const exts = await (await fetch('/api/extensions')).json();
    return (exts as any[]).find((e) => e.name === name)?.id || '';
  }, EXT_NAME);
}

/** Give the fixture a config value the host hands it on init. */
async function setFixtureConfig(page: Page, id: string): Promise<void> {
  const res = await csrfFetch(page, `/api/extensions/${id}`, {
    method: 'PUT',
    body: { config: { note: SECRET_CONFIG } },
  });
  if (!res.ok) throw new Error(`set fixture config failed (${res.status})`);
}

/** Open this spec's project + file in the editor and wait for the editor. */
async function openFixtureFile(page: Page): Promise<void> {
  await page.goto('/');
  await page.waitForSelector('.project-item', { timeout: 15000 });
  await page.locator('.project-item', { hasText: PROJECT_NAME }).first().click();
  await page.waitForSelector('.top-bar', { timeout: 15000 });
  await page.locator('.explorer-btn').click();
  await expect(page.locator('.file-browser-list')).toBeVisible({ timeout: 15000 });
  await page.locator('.file-browser-item', { hasText: DOC_FILE }).first().click();
  await expect(page.locator('.file-editor .cm-editor')).toBeVisible({ timeout: 15000 });
}

/** The `?extch=` token the host put in its iframe URL for this mount. */
async function mountToken(page: Page): Promise<string> {
  const src = await page.locator('iframe[src*="extch="]').first().getAttribute('src');
  expect(src, 'host iframe carries a per-mount channel token').toMatch(/[?&]extch=[0-9a-f]{32}$/);
  return (src as string).slice((src as string).lastIndexOf('extch=') + 6);
}

/** The frame holding the document the host mounted (matched by its mount token). */
async function mountedFrame(page: Page, token: string): Promise<Frame> {
  for (let i = 0; i < 100; i++) {
    const f = page.frames().find(fr => fr.url().includes(`extch=${token}`));
    if (f && (await f.locator('#log').textContent().catch(() => ''))?.includes('ARMED')) return f;
    await page.waitForTimeout(100);
  }
  throw new Error('the mounted extension document never armed its channel');
}

/** The same-origin sibling document that took over the frame. */
async function siblingFrame(page: Page): Promise<Frame> {
  for (let i = 0; i < 150; i++) {
    const f = page.frames().find(fr => fr.url().endsWith('/other.html'));
    if (f && (await f.locator('#log').textContent().catch(() => ''))?.includes('AT-SIBLING')) return f;
    await page.waitForTimeout(100);
  }
  throw new Error('the frame never reached the same-origin sibling document');
}

const frameLog = (frame: Frame) => frame.locator('#log').textContent() as Promise<string>;

/** Type into the open file — while armed, every keystroke pushes editor.contentChange. */
async function typeInEditor(page: Page, text: string): Promise<void> {
  await page.locator('.file-editor .cm-content').click();
  await page.keyboard.press('End');
  await page.keyboard.type(text);
  await page.waitForTimeout(600);
}

test.describe.serial('Extension frame channel (document identity)', () => {
  let extId = '';

  test.beforeAll(async ({ browser }) => {
    rmSync(REPO, { recursive: true, force: true });
    mkdirSync(REPO, { recursive: true });
    writeFileSync(join(REPO, DOC_FILE), `first line\n${SECRET}\n`);
    execSync('git init', { cwd: REPO, stdio: 'ignore' });
    execSync('git add -A', { cwd: REPO, stdio: 'ignore' });
    execSync('git -c user.email="test@kanban.local" -c user.name="Test" commit -m "Initial commit"', {
      cwd: REPO, stdio: 'ignore',
    });

    // The hostile origin. Every request it serves is counted — any hit is a leak.
    payloadServer = createServer((req, res) => {
      payloadHits++;
      if (req.url === '/payload.js') {
        res.writeHead(200, { 'Content-Type': 'text/javascript' });
        res.end(HIJACK_SCRIPT);
      } else {
        res.writeHead(200, { 'Content-Type': 'text/html' });
        res.end(`<!doctype html><html><body>
<div id="log">AT-HOSTILE</div>
<script src="payload.js"></` + `script>
</body></html>`);
      }
    });
    await new Promise<void>((r) => payloadServer.listen(0, '127.0.0.1', r));
    payloadBase = `http://127.0.0.1:${(payloadServer.address() as { port: number }).port}`;

    const { context, page } = await setupPage(browser);
    try {
      const res = await csrfFetch(page, '/api/projects', { method: 'POST', body: { path: REPO } });
      if (!res.ok) throw new Error(`create project failed (${res.status})`);
    } finally {
      await context.close();
    }
  });

  test.afterAll(async ({ browser }) => {
    const { context, page } = await setupPage(browser);
    try {
      if (!extId) extId = await getFixtureExtId(page);
      if (extId) await csrfFetch(page, `/api/extensions/${extId}`, { method: 'DELETE' });
      const list = await page.evaluate(async () => await (await fetch('/api/projects')).json());
      const mine = Array.isArray(list)
        ? list.find((p: any) => p.name === PROJECT_NAME || p.path === REPO) : undefined;
      if (mine) await csrfFetch(page, `/api/projects/${(mine as any).id}`, { method: 'DELETE' });
    } finally {
      await context.close();
    }
    rmSync(EXT_DIR_PATH, { recursive: true, force: true });
    // The author dir exists only for this fixture — drop it when empty (an
    // interrupted run must not leave litter in the repo's extensions dir).
    try { rmdirSync(join(EXTENSIONS_DIR, AUTHOR)); } catch { /* not empty / gone */ }
    rmSync(REPO, { recursive: true, force: true });
    await new Promise<void>((r) => payloadServer.close(() => r()));
  });

  test('the mounted document handshakes and receives init', async ({ page, browser }) => {
    writeFixtureExtension('stay');
    const setup = await setupPage(browser);
    // Install + configure on a page of our own, then load the app fresh.
    await installFixture(setup.page);
    extId = await getFixtureExtId(setup.page);
    expect(extId).not.toBe('');
    await setFixtureConfig(setup.page, extId);
    await setup.context.close();

    await openFixtureFile(page);
    const token = await mountToken(page);
    expect(token).toHaveLength(32);
    const log = await frameLog(await mountedFrame(page, token));

    // init arrived: the frame knows its grants and the host-provided context…
    expect(log).toContain('ARMED granted=');
    expect(log).toContain('config.get');
    // …the host's own injection context, not something the frame chose…
    expect(log).toContain('"filePath":"');
    expect(log).toContain(DOC_FILE);
    // …and its own config.
    expect(log).toContain(SECRET_CONFIG);

    // And the channel really is live: typing pushes the open file into the
    // frame. This is precisely the payload the next two tests must not deliver.
    await typeInEditor(page, ' typed-more');
    const live = await frameLog(await mountedFrame(page, token));
    expect(live).toContain('editor.contentChange');
    expect(live).toContain(SECRET);
  });

  test('a same-origin sibling document in the frame is disarmed and learns nothing', async ({ page, browser }) => {
    writeFixtureExtension('sibling');
    const consoleLines: string[] = [];
    page.on('console', m => consoleLines.push(m.text()));

    await openFixtureFile(page);
    let hostile = await siblingFrame(page);
    expect(await frameLog(hostile)).toContain('AT-SIBLING');  // the escape happened

    // Typing pushes editor.contentChange at the frame — the sibling must not
    // see the open file.
    await typeInEditor(page, ' typed-after-escape');
    hostile = await siblingFrame(page);
    const log = await frameLog(hostile);

    // Nothing the host pushes reached it: no init payload (grants / config /
    // context), no file content, not even a call response. (The host's handshake
    // REQUEST is delivered — it carries no data.)
    expect(log).not.toContain('"type":"init"');
    expect(log).not.toContain('editor.contentChange');
    expect(log).not.toContain(SECRET);
    expect(log).not.toContain(SECRET_CONFIG);
    expect(log).not.toContain('"type":"response"');
    // Its forged hello echoed nothing back, and its forged call went unanswered.
    expect(log).not.toContain('guessed');
    expect(log).not.toContain('evil-1');

    // The frame had genuinely been armed before escaping: its log.write call was
    // executed by the host. This is the escape-after-trust chain, not a channel
    // that simply never opened.
    expect(consoleLines.some(w => w.includes(PRE_ESCAPE_TAG))).toBe(true);
    // Warn-only watchdog: it reported the reload, refused the forgery, and the
    // iframe is still mounted (warn + disarm — never teardown).
    expect(consoleLines.some(w => /extension frame reloaded/.test(w))).toBe(true);
    expect(consoleLines.some(w => /channel handshake rejected/.test(w))).toBe(true);
    expect(consoleLines.some(w => /un-handshaked document/.test(w))).toBe(true);
    await expect(page.locator('iframe[src*="extch="]').first()).toHaveCount(1);
  });

  test('a cross-origin self-navigation is blocked in flight and the channel disarms', async ({ page, browser }) => {
    // Written here, not in beforeAll: the fixture needs the payload server's port.
    writeFixtureExtension('cross-origin');
    const consoleLines: string[] = [];
    page.on('console', m => consoleLines.push(m.text()));
    payloadHits = 0;

    await openFixtureFile(page);
    const token = await mountToken(page);
    // Wait until the document was armed and had escaped (its frame is gone).
    await expect.poll(async () => {
      const lines = consoleLines.join('\n');
      return lines.includes(PRE_ESCAPE_TAG) && !page.frames().some(f => f.url().includes(`extch=${token}`));
    }, { timeout: 20000 }).toBe(true);

    // The hostile origin was never contacted: the host CSP's frame-src 'self'
    // refuses the navigation before a request leaves the browser.
    expect(payloadHits, `requests that reached the hostile origin: ${payloadHits}`).toBe(0);
    expect(page.frames().some(f => f.url().startsWith(payloadBase))).toBe(false);
    // The watchdog still saw the (failed) load and disarmed the channel.
    expect(consoleLines.some(w => /extension frame reloaded/.test(w))).toBe(true);
    // And the iframe survives — warn + disarm, never teardown.
    await expect(page.locator('iframe[src*="extch="]').first()).toHaveCount(1);
  });

  test('navigating the extension\'s OWN nested iframes keeps the channel armed', async ({ page, browser }) => {
    // The false-positive guard. html-preview (and anything else that renders
    // into a child frame) navigates a nested iframe on every refresh; those
    // loads belong to the nested browsing context and must NOT be read as the
    // mounted document reloading — otherwise the watchdog would disarm the very
    // channel the extension needs to keep working.
    writeFixtureExtension('nested');
    const consoleLines: string[] = [];
    page.on('console', m => consoleLines.push(m.text()));

    await openFixtureFile(page);
    const token = await mountToken(page);
    // The mounted document (the one the host armed) is still the mounted document
    // after three nested-frame navigations.
    const frame = await mountedFrame(page, token);
    await expect.poll(() => frameLog(frame), { timeout: 20000 }).toContain(NESTED_DONE_TAG);

    // No reload was reported…
    expect(consoleLines.join('\n')).not.toMatch(/extension frame reloaded/);
    expect(consoleLines.join('\n')).not.toMatch(/channel handshake rejected/);
    // …and the channel is still live: a keystroke still pushes the file in.
    await typeInEditor(page, ' still-live');
    const after = await frameLog(await mountedFrame(page, token));
    expect(after).toContain('editor.contentChange');
    expect(after).toContain('still-live');
  });

  test('an extension document visited outside the host\'s frame cannot reach the session', async ({ browser }) => {
    // An extension entry is also a plain URL on this origin, reachable outside the frame
    // the host mounts. docs/ui-extensions.md.
    const { context, page } = await setupPage(browser);
    try {
      // Rewrite the fixture so this test does not depend on the previous one's files.
      writeFixtureExtension('stay');
      await page.goto(SIBLING_URL);
      await expect(page.locator('#log')).toContainText('AT-SIBLING');

      // A sandboxed document has no origin of its own, so storage is refused.
      expect(await page.evaluate(() => {
        try { window.localStorage.getItem('kanban-storage'); return 'readable'; } catch { return 'blocked'; }
      })).toBe('blocked');
      // And the endpoint that turns same-origin code into API access is unreachable.
      expect(await page.evaluate(async () => {
        try {
          const res = await fetch('/api/auth/session', { credentials: 'include' });
          return `${res.status} ${JSON.stringify(await res.json())}`;
        } catch { return 'blocked'; }
      })).toBe('blocked');
    } finally {
      await context.close();
    }
  });
});
