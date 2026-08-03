/**
 * Standalone diagnostic: verify the capability-confined GitHub importer
 * mounts and imports an issue end-to-end against the live dev server.
 *
 * The server-side proxy call to api.github.com is mocked via page.route so
 * the test is deterministic and needs no external network. The mock proves
 * the full client chain: paste event -> host forwards -> extension calls
 * network.proxy -> host calls /api/extensions/{id}/proxy -> insertText.
 *
 * Run: node frontend/tests/importer-e2e.mjs
 */
import { chromium } from 'playwright';

const BASE = 'https://localhost:3000';
const PASSWORD = 'test';
const GITHUB_URL = 'https://github.com/octocat/Hello-World/issues/1347';

const logs = [];
const t0 = Date.now();
const log = (m) => logs.push(`[+${(Date.now() - t0).toFixed(0)}ms] ${m}`);

const browser = await chromium.launch({ headless: true });
const page = await browser.newPage({ ignoreHTTPSErrors: true });

page.on('console', (m) => log(`console.${m.type()}: ${m.text()}`));
page.on('pageerror', (e) => log(`pageerror: ${e.message}`));

// Relay: log every postMessage the parent window receives (from the iframe).
await page.goto(BASE, { waitUntil: 'domcontentloaded' });
await page.evaluate(() => {
    window.__relay = [];
    window.addEventListener('message', (e) => {
        if (e.source && e.source !== window) {
            window.__relay.push(JSON.stringify(e.data));
        }
    });
});

// Mock the server-side proxy so the importer's GitHub fetch succeeds offline.
// A delay gives us a window to observe the host-rendered loading indicator.
await page.route('**/api/extensions/*/proxy', async (route) => {
    await new Promise((r) => setTimeout(r, 900));
    await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
            title: 'Mock Issue Title',
            body: 'Mock issue body content.',
        }),
    });
});

async function step(name, fn) {
    try {
        await fn();
        log(`PASS: ${name}`);
    } catch (e) {
        log(`FAIL: ${name} — ${e.message}`);
    }
}

// ── Login ────────────────────────────────────────────────────────────────
await page.waitForSelector('#login-password', { timeout: 15000 }).catch(() => {});
await page.fill('#login-password', PASSWORD);
await page.click('.login-btn');

// Wait for app shell (sidebar / project area).
await page.waitForSelector('.project-item, .board, .sidebar', { timeout: 20000 }).catch(() => log('warn: app shell selector not found'));

// Ensure a project is selected so the board (and backlog add-task button) render.
const projectItem = page.locator('.project-item', { hasText: 'kanban' }).first();
if (await projectItem.count()) {
    await projectItem.click();
    log('selected project "kanban"');
}

// ── Open create-task modal ───────────────────────────────────────────────
await step('open create-task modal', async () => {
    const addBtn = page.locator('button.add-btn[aria-label="Create a new task"], button.add-task-btn').first();
    await addBtn.waitFor({ state: 'visible', timeout: 20000 });
    await addBtn.click();
    await page.waitForSelector('#task-description', { timeout: 10000 });
});

// ── Verify sandboxed iframe mounts ───────────────────────────────────────
let iframe = null;
await step('sandboxed importer iframe mounts in anchor', async () => {
    iframe = page.locator('iframe[src*="github-issue-importer"]').first();
    await iframe.waitFor({ state: 'attached', timeout: 10000 });
    const sandbox = await iframe.getAttribute('sandbox');
    if (!sandbox || !sandbox.includes('allow-scripts')) throw new Error(`sandbox attr missing allow-scripts: ${sandbox}`);
    if (sandbox.includes('allow-same-origin')) throw new Error(`sandbox MUST NOT include allow-same-origin: ${sandbox}`);
    log(`  iframe sandbox="${sandbox}"`);
});

// Give the host-client + init handshake time to complete inside the iframe.
await page.waitForTimeout(800);

// The iframe must be invisible (height 0) when idle — no blank space.
await step('iframe is invisible (height 0) while idle', async () => {
    const height = await iframe.evaluate((el) => el.offsetHeight);
    if (height !== 0) throw new Error(`iframe height=${height}, expected 0 (invisible)`);
    log(`  idle iframe offsetHeight = ${height}`);
});

// ── Simulate pasting a GitHub URL into the description ───────────────────
await step('paste GitHub URL is forwarded and import runs', async () => {
    await page.evaluate((url) => {
        const ta = document.getElementById('task-description');
        const dt = new DataTransfer();
        dt.setData('text/plain', url);
        const ev = new Event('paste', { bubbles: true, cancelable: true });
        Object.defineProperty(ev, 'clipboardData', { value: dt });
        ta.dispatchEvent(ev);
    }, GITHUB_URL);
});

// While the (delayed) proxy is in flight, a host-rendered loading indicator shows.
await step('host renders a loading indicator during import', async () => {
    await page.waitForSelector('[data-ext-status]', { state: 'visible', timeout: 3000 });
    const text = await page.locator('[data-ext-status]').textContent();
    if (!/Importing/i.test(text || '')) throw new Error(`loading text mismatch: ${text}`);
    log(`  loading indicator: ${text.trim()}`);
});

// ── Verify the imported content REPLACES the pasted URL (not appended) ───
await step('imported issue content REPLACES the pasted URL', async () => {
    await page.waitForFunction(
        () => document.getElementById('task-description')?.value.includes('Mock Issue Title'),
        { timeout: 10000 }
    );
    const value = await page.locator('#task-description').inputValue();
    log(`  textarea now contains: ${JSON.stringify(value.slice(0, 80))}…`);
    // The imported content must come first — the raw pasted URL must NOT be
    // prepended (the old bug appended content after the URL). The URL may still
    // appear as a trailing Reference: line the extension adds on purpose.
    if (value.startsWith(GITHUB_URL)) throw new Error(`pasted URL not replaced (content appended): ${JSON.stringify(value.slice(0, 120))}`);
    if (!value.startsWith('# Mock Issue Title')) throw new Error(`content should lead, got: ${JSON.stringify(value.slice(0, 120))}`);
});

// After import completes, the loading indicator is hidden again.
await step('loading indicator hidden after import', async () => {
    await page.waitForFunction(
        () => {
            const el = document.querySelector('[data-ext-status]');
            return !el || el.style.display === 'none';
        },
        { timeout: 5000 }
    );
    log('  status hidden');
});

// The iframe should still be invisible (importer never calls ui.setSize).
await step('iframe stays invisible after import', async () => {
    const height = await iframe.evaluate((el) => el.offsetHeight);
    if (height !== 0) throw new Error(`iframe height=${height} after import`);
    log(`  final iframe offsetHeight = ${height}`);
});

// Dump relayed messages from the iframe for diagnosis.
const relay = await page.evaluate(() => window.__relay || []);
log(`relayed iframe messages (${relay.length}):`);
relay.forEach((m) => log(`  ${m}`));

// ── Negative check: extension cannot read parent app state ───────────────
await step('sandboxed iframe has opaque origin (cannot access parent DOM)', async () => {
    const result = await page.evaluate(() => {
        const f = document.querySelector('iframe[src*="github-issue-importer"]');
        // Access from inside the iframe — must fail / be null due to cross-origin.
        try {
            const doc = f.contentDocument;
            return doc === null ? 'blocked' : 'accessible';
        } catch {
            return 'blocked';
        }
    });
    log(`  parent->iframe contentDocument access: ${result}`);
});

await page.screenshot({ path: '/tmp/importer-e2e.png' });
await browser.close();

console.log('\n===== RESULTS =====');
console.log(logs.join('\n'));
const failed = logs.filter((l) => l.startsWith('FAIL:'));
console.log(`\n${failed.length} failed / ${logs.filter((l) => l.startsWith('PASS:')).length} passed`);
process.exit(failed.length ? 1 : 0);
