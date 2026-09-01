// Live check for the sandbox-image fix, run against a dev server on a project whose image is built
// from a Dockerfile and is absent from local storage:
//
//   make run && node frontend/tests/sandbox-image-live-check.mjs
//
//  1. Opening the explorer builds the image and the blocked request comes back with a listing.
//  2. Progress arrives as `sandbox_image_status` frames and renders in .sandbox-image-status.
//  3. Podman's masked wording ("short-name resolution ... TTY") never reaches the browser.
//  4. The second visit is instant and silent.
//
// Env: BASE (default http://localhost:3000), PASSWORD (default test), PROJECT (default getmike.dev).
import { chromium } from '@playwright/test';

const BASE = process.env.BASE || 'http://localhost:3000';
const PASSWORD = process.env.PASSWORD || 'test';
const PROJECT = process.env.PROJECT || 'getmike.dev';

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const browser = await chromium.launch();
try {
    const ctx = await browser.newContext();
    const page = await ctx.newPage();

    // Record received frames and /api/fs timings from inside the page.
    await page.addInitScript(() => {
        window.__frames = [];
        window.__fs = [];
        const Native = window.WebSocket;
        window.WebSocket = class extends Native {
            constructor(url, protocols) {
                super(url, protocols);
                this.addEventListener('message', (ev) => {
                    let msg = null;
                    try { msg = JSON.parse(ev.data); } catch { return; }
                    if (msg && msg.type === 'sandbox_image_status') {
                        window.__frames.push({ t: Date.now(), ...(msg.data || {}) });
                    }
                });
            }
        };
        const nativeFetch = window.fetch.bind(window);
        window.fetch = async (input, init) => {
            const url = String(typeof input === 'string' ? input : input.url);
            const started = Date.now();
            const res = await nativeFetch(input, init);
            if (url.includes('/api/fs')) {
                window.__fs.push({ url, ms: Date.now() - started, status: res.status });
            }
            return res;
        };
    });

    await page.goto(BASE, { waitUntil: 'domcontentloaded' });
    const login = await page.evaluate(async (password) => {
        const res = await fetch('/api/auth/login', {
            method: 'POST',
            credentials: 'include',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ password }),
        });
        return res.status;
    }, PASSWORD);
    if (login !== 200) throw new Error(`login failed with status ${login}`);

    const url = `${BASE}/?project=${encodeURIComponent(PROJECT)}&view=explorer`;
    console.log(`opening ${url}`);
    const openedAt = Date.now();
    await page.goto(url, { waitUntil: 'domcontentloaded' });

    // The listing is blocked until the image exists, so the banner appears first — or nothing does,
    // if the image was already there.
    let sawBanner = false;
    let bannerLabel = '';
    let bannerLog = '';
    let bannerAt = 0;
    for (let i = 0; i < 240; i++) {
        const banner = await page.locator('.sandbox-image-status').first();
        if (await banner.count()) {
            sawBanner = true;
            bannerAt = Date.now() - openedAt;
            bannerLabel = (await banner.locator('.sandbox-image-status-label').textContent()) || '';
            bannerLog = (await banner.locator('.sandbox-image-status-log').textContent().catch(() => '')) || '';
            if (bannerLog.length > 40) break; // progress is flowing
        }
        // An already-built image answers without ever announcing anything: the listing itself is
        // the signal to stop watching (waiting a fixed number of polls would report the script's
        // patience as if it were the app's latency).
        if (!sawBanner && (await page.locator('.file-browser-item').count()) > 0) break;
        await sleep(250);
    }

    // A build can take minutes.
    await page.waitForSelector('.file-browser-item', { timeout: 600_000 });
    const listedAt = Date.now() - openedAt;

    const report = await page.evaluate(() => ({
        frames: window.__frames,
        fs: window.__fs,
        bodyHasMaskedPodmanText: /(short-name resolution|cannot prompt without a TTY)/i.test(document.body.innerText),
    }));

    const phases = report.frames.map((f) => f.phase);
    const lineCount = report.frames.filter((f) => f.line).length;
    const fsCalls = report.fs;
    const slowest = fsCalls.reduce((a, f) => Math.max(a, f.ms), 0);

    console.log(`\n--- first visit (image ${phases.length ? 'absent, so built here' : 'already present'}) ---`);
    console.log(`banner visible:      ${sawBanner} (at +${bannerAt} ms)`);
    console.log(`banner label:        ${bannerLabel}`);
    console.log(`banner log tail:     ${bannerLog.split('\n').slice(-3).join(' | ')}`);
    console.log(`listing rendered at: +${listedAt} ms`);
    console.log(`status frames:       ${phases.length} [${phases[0]} … ${phases[phases.length - 1]}], ${lineCount} with output lines`);
    console.log(`/api/fs calls:       ${fsCalls.length}, slowest ${slowest} ms, statuses ${[...new Set(fsCalls.map((f) => f.status))].join(',')}`);

    const problems = [];
    if (report.bodyHasMaskedPodmanText) problems.push('podman short-name wording reached the browser');
    if (fsCalls.some((f) => f.status >= 400)) problems.push('the explorer request failed: ' + JSON.stringify(fsCalls));
    if (!fsCalls.length) problems.push('no /api/fs request was observed');
    if (phases.length) {
        if (phases[0] !== 'building_image') problems.push(`first frame was ${phases[0]}, want building_image`);
        if (phases[phases.length - 1] !== 'done') problems.push(`last frame was ${phases[phases.length - 1]}, want done`);
        if (!lineCount) problems.push('no build output lines reached the browser');
        if (!sawBanner) problems.push('frames arrived but the banner never rendered');
        if (!/Building sandbox image/.test(bannerLabel)) problems.push(`banner label was "${bannerLabel}"`);
    } else {
        console.log('(no build happened — the image was already present; run again after `podman rmi localhost/sandbox-' + PROJECT + '`)');
    }

    // Second visit: the image exists, so nothing may be announced and nothing may stall.
    await page.evaluate(() => { window.__frames = []; window.__fs = []; });
    const secondStart = Date.now();
    await page.goto(`${BASE}/?project=${encodeURIComponent('MIKE')}`, { waitUntil: 'domcontentloaded' });
    await sleep(1000);
    await page.goto(url, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('.file-browser-item', { timeout: 60_000 });
    const secondMs = Date.now() - secondStart;
    const second = await page.evaluate(() => ({ frames: window.__frames, fs: window.__fs }));
    const bannerSecond = await page.locator('.sandbox-image-status').count();

    console.log('\n--- second visit (image present) ---');
    console.log(`listing rendered at: +${secondMs} ms`);
    console.log(`status frames:       ${second.frames.length}`);
    console.log(`banner present:      ${bannerSecond > 0}`);
    console.log(`/api/fs calls:       ${second.fs.length}, slowest ${second.fs.reduce((a, f) => Math.max(a, f.ms), 0)} ms`);

    if (second.frames.length) problems.push(`second visit announced ${JSON.stringify(second.frames.map((f) => f.phase))}`);
    if (bannerSecond > 0) problems.push('the banner is still on screen after a successful build');
    if (secondMs > 15_000) problems.push(`second visit took ${secondMs} ms`);

    const verdict = problems.length
        ? 'PROBLEMS:\n - ' + problems.join('\n - ')
        : phases.length
            ? 'OK: image built on demand, progress streamed, second visit instant and silent'
            : 'OK: image already present, no build announced, both visits instant';
    console.log('\n' + verdict);
    process.exitCode = problems.length ? 1 : 0;
} finally {
    await browser.close();
}
