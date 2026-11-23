import { test, expect, type Page, type Frame } from '@playwright/test';
import { mkdirSync, rmSync, rmdirSync, writeFileSync } from 'node:fs';
import { execSync } from 'node:child_process';
import { dirname, join, resolve } from 'path';
import { fileURLToPath } from 'url';
import { csrfFetch, selectProject, signedInPage } from './helpers';

// The binary preview across all three boundaries: a file the server refuses as text gains a Preview
// button, and clicking it hands an extension frame the file's real bytes. The middle boundary is where
// the bug lives — the host's `post()` round-trips its reply through `JSON.stringify` to strip Svelte
// `$state` proxies, and a `Blob` run through that arrives as `{}` — so the byte reply rides its own
// frame (`{ type: 'bytes', id, byteLength, blob }`) and structured clone carries it.
//
// What no unit test reaches: no frame exists while a previewable file is merely open, the click mounts
// one, and the frame reads the file's exact length and leading bytes. The route's refusals (undeclared
// suffix, over-cap, `.git`, outside the jail) are `backend/server/handler_fs_bytes_test.go`, and the
// modal's own states are `components/__tests__/binary-preview-diff.unit.svelte.ts`.
//
// Installs a fixture extension into a dedicated repo and project, so it races no other spec's editor or
// extension list, and starts no turn, so it holds no task. The spec authors the fixture, so it lives in
// `extensions/` and is deleted with the project (docs/testing.md).

const TS = Date.now();
const AUTHOR = 'binprev-fixture';
// Lowercase + hyphens only: the name goes into a URL path verbatim and frames are matched on it.
const EXT_NAME = `bytes-test-${TS}`;
const REPO = '/tmp/kanban-test-repo-binprev';
const PROJECT_NAME = 'kanban-test-repo-binprev';

/** The suffix the fixture declares, and the file that carries it. Never a real format name. */
const SUFFIX = 'kbin';
const DECLARED_FILE = `model.${SUFFIX}`;
/** Binary, but no installed extension claims it — the state that must look exactly as it did before. */
const UNDECLARED_FILE = 'notes.zzz';

/** Leading bytes of the fixture file: a NUL in there is what makes the read refuse it as text. */
const MARKER = [0x4d, 0x4b, 0x53, 0x48, 0x00, 0xff, 0x7f, 0x10];
const SIZE = 4096;
const PAYLOAD = (() => {
    const b = Buffer.alloc(SIZE);
    MARKER.forEach((v, i) => { b[i] = v; });
    for (let i = MARKER.length; i < SIZE; i++) b[i] = (i * 37 + 11) & 0xff;
    return b;
})();
/** What the frame must report back: the file's own length and its own leading bytes. */
const EXPECTED_HEAD = MARKER.join(',');

/** Where the Go server scans for installable local extensions. */
const HERE = dirname(fileURLToPath(import.meta.url));
const EXTENSIONS_DIR = process.env.MIKE_EXTENSIONS_DIR
  || resolve(HERE, '..', '..', '..', 'extensions');
const EXT_DIR_PATH = join(EXTENSIONS_DIR, AUTHOR, EXT_NAME);

const MANIFEST = JSON.stringify({
    display_name: 'Byte Fixture',
    description: 'Test fixture: renders a file format the core knows nothing about.',
    author: AUTHOR,
    version: '0.0.1',
    priority: 0,
    capabilities: {
        ui: {
            injection_points: ['binaryPreview'],
            file_suffixes: [SUFFIX],
            permissions: ['fs.getBytes'],
        },
    },
}, null, 4);

/**
 * The extension's document: reports its grants, the mount context that named its file, and the bytes it
 * was handed. Every script is an external file — the extension CSP blocks an inline one.
 */
function writeFixtureExtension(): void {
    mkdirSync(EXT_DIR_PATH, { recursive: true });
    writeFileSync(join(EXT_DIR_PATH, 'manifest.json'), MANIFEST);
    writeFileSync(join(EXT_DIR_PATH, 'index.html'), `<!doctype html><html><body>
<div id="log"></div>
<script src="/host-client.js"></` + `script>
<script src="main.js?v=${TS}"></` + `script>
</body></html>`);
    writeFileSync(join(EXT_DIR_PATH, 'main.js'), `
var el = document.getElementById('log');
function log(s) { el.textContent += s + '\\n'; }
window.extHost.ready().then(function () {
  log('ARMED ' + window.extHost.granted.slice().sort().join(','));
  log('CONTEXT ' + JSON.stringify(window.extHost.context));
  return window.extHost.fs.getBytes('working');
}).then(function (r) {
  log('KIND ' + Object.prototype.toString.call(r && r.blob));
  log('META byteLength=' + (r && r.byteLength));
  return r.blob.arrayBuffer();
}).then(function (buf) {
  var b = new Uint8Array(buf);
  log('BYTES n=' + b.length + ' head=' + Array.prototype.slice.call(b.subarray(0, 8)).join(','));
}).catch(function (err) { log('ERR ' + (err && err.message)); });
`);
}

// ── helpers ──────────────────────────────────────────────────────────
/** `browser.newContext()` does not reliably inherit the project's baseURL. */
const BASE_URL = 'https://localhost:3001';
const STORAGE_STATE = '/tmp/kanban-test-storage-state.json.isolated';

/** install-local the fixture (the response is a stream, so the fetch resolves when it ends). */
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

/** Open this spec's project and file in the editor, waiting for the binary notice. */
async function openFixtureFile(page: Page, file: string): Promise<void> {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 15000 });
    // A row is a link; clicking the row's box would land on the sandbox badge beside it.
    await selectProject(page, projectId);
    await page.waitForSelector('.top-bar', { timeout: 15000 });
    await page.locator('.explorer-btn').click();
    await expect(page.locator('.file-browser-list')).toBeVisible({ timeout: 15000 });
    await page.locator('.file-browser-item', { hasText: file }).first().click();
    await expect(page.locator('.editor-content .editor-placeholder'))
        .toContainText('Binary file', { timeout: 15000 });
}

/** The `?extch=` token of the frame the host mounted, or '' when no frame is mounted. */
async function mountToken(page: Page): Promise<string> {
    const frames = page.locator('iframe[src*="extch="]');
    // count() answers now; getAttribute would auto-wait for a frame this assertion says must not exist.
    if ((await frames.count()) === 0) return '';
    const src = await frames.first().getAttribute('src');
    return src ? src.slice(src.lastIndexOf('extch=') + 6) : '';
}

/** The frame the host mounted, once its document has reported something. */
async function mountedFrame(page: Page, token: string): Promise<Frame> {
    for (let i = 0; i < 150; i++) {
        const f = page.frames().find(fr => fr.url().includes(`extch=${token}`));
        if (f && (await f.locator('#log').textContent().catch(() => ''))?.includes('ARMED')) return f;
        await page.waitForTimeout(100);
    }
    throw new Error('the preview frame never armed its channel');
}

const frameLog = (frame: Frame) => frame.locator('#log').textContent() as Promise<string>;

let extId = '';
let projectId = '';

test.describe.serial('Binary preview (a file the core knows nothing about)', () => {

    test.beforeAll(async ({ browser }) => {
        rmSync(REPO, { recursive: true, force: true });
        mkdirSync(REPO, { recursive: true });
        writeFileSync(join(REPO, DECLARED_FILE), PAYLOAD);
        writeFileSync(join(REPO, UNDECLARED_FILE), Buffer.from([0x00, 0x01, 0x02, 0x03]));
        execSync('git init', { cwd: REPO, stdio: 'ignore' });
        execSync('git add -A', { cwd: REPO, stdio: 'ignore' });
        execSync('git -c user.email="test@kanban.local" -c user.name="Test" commit -m "Initial commit"', {
            cwd: REPO, stdio: 'ignore',
        });
        writeFixtureExtension();

        const { context, page } = await signedInPage(browser, { storageState: STORAGE_STATE, baseURL: BASE_URL });
        try {
            const created = await csrfFetch(page, '/api/projects', { method: 'POST', body: { path: REPO } });
            if (!created.ok) throw new Error(`create project failed (${created.status})`);
            projectId = created?.data?.id || '';
            if (!projectId) throw new Error('the fixture project was created without an id');
            await installFixture(page);
            extId = await getFixtureExtId(page);
            if (!extId) throw new Error('the fixture extension was never installed');
        } finally {
            await context.close();
        }
    });

    test.afterAll(async ({ browser }) => {
        const { context, page } = await signedInPage(browser, { storageState: STORAGE_STATE, baseURL: BASE_URL });
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
        // The author dir exists only for this fixture — drop it when empty.
        try { rmdirSync(join(EXTENSIONS_DIR, AUTHOR)); } catch { /* not empty / gone */ }
        rmSync(REPO, { recursive: true, force: true });
    });

    test('a suffix no installed extension declared keeps the plain notice', async ({ page }) => {
        await openFixtureFile(page, UNDECLARED_FILE);
        await expect(page.locator('.editor-content .editor-preview-binary')).toHaveCount(0);
        expect(await mountToken(page), 'no frame for an unclaimed suffix').toBe('');
    });

    test('no byte-capable frame exists while the file is merely open', async ({ page }) => {
        await openFixtureFile(page, DECLARED_FILE);
        await expect(page.locator('.editor-content .editor-preview-binary')).toBeVisible({ timeout: 15000 });
        expect(await mountToken(page), 'the channel is built with the modal, not with the file').toBe('');
    });

    test('the preview mounts the frame and the frame is handed the file', async ({ page }) => {
        await openFixtureFile(page, DECLARED_FILE);
        await page.locator('.editor-content .editor-preview-binary').click();

        // The dialog is never a unique locator — the Explorer panel is a dialog too and lists the same
        // name — so it is named by its modal class as well as by the file it is previewing.
        const dialog = page.locator('div.modal[role="dialog"]', { hasText: DECLARED_FILE });
        await expect(dialog).toBeVisible({ timeout: 15000 });
        await expect(page.locator('iframe[src*="extch="]')).toHaveCount(1, { timeout: 15000 });

        const token = await mountToken(page);
        expect(token, 'the mounted frame carries a per-mount channel token').toHaveLength(32);
        const log = await frameLog(await mountedFrame(page, token));

        // The mount, not the frame, named the file.
        expect(log).toContain('ARMED');
        expect(log).toContain('fs.getBytes');
        expect(log).toContain(`"repoPath":"${DECLARED_FILE}"`);
        // A `Blob` that survived structured clone, not the `{}` a JSON round-trip leaves behind.
        expect(log).not.toContain('ERR');
        expect(log).toContain('KIND [object Blob]');
        expect(log).toContain(`META byteLength=${SIZE}`);
        // The file's own length and its own leading bytes: base64 would be longer and ASCII.
        expect(log).toContain(`BYTES n=${SIZE} head=${EXPECTED_HEAD}`);
    });
});
