// Needs a live server (`make run`). Mounts an extension entry the way ui-injector
// does — sandbox="allow-scripts", ?extch= — and requires the document to echo the
// mount token, which it can only do if it fetched and ran /host-client.js.
// Fails as a blocked /host-client.js if that file ever gets CORP-marked again.
import { chromium } from 'playwright';

const BASE = process.env.BASE || 'https://localhost:3000';
const EXT = process.env.EXT || '/extensions/softwarerenderer/html-preview/';
const TOKEN = 'probe-' + Date.now();

const browser = await chromium.launch({ args: ['--ignore-certificate-errors'] });
const ctx = await browser.newContext({ ignoreHTTPSErrors: true });

ctx.on('console', (m) => {
    if (m.type() === 'error' || m.type() === 'warning') {
        const loc = m.location() || {};
        console.log(`[console:${m.type()}] ${m.text()}  @${loc.url}:${loc.lineNumber}`);
    }
});
ctx.on('pageerror', (e) => console.log('[pageerror]', e.message));
ctx.on('requestfailed', (r) => console.log('[FAILED]', r.url(), r.failure()?.errorText));
ctx.on('response', (r) => {
    const u = r.url();
    if (r.status() >= 400 || u.endsWith('host-client.js') || u.endsWith('Preview.js'))
        console.log(`[resp:${r.status()}]`, u);
});

const host = await ctx.newPage();
await host.goto(BASE + '/', { waitUntil: 'commit' });
await host.waitForTimeout(800);

const hello = await host
    .evaluate(async ({ ext, token }) => {
        window.__msgs = [];
        window.addEventListener('message', (e) => window.__msgs.push(e.data));
        const f = document.createElement('iframe');
        // What ui-injector.svelte.ts sets on a mounted extension frame.
        f.setAttribute('sandbox', 'allow-scripts');
        f.src = `${ext}?extch=${token}`;
        f.style.cssText = 'width:600px;height:400px';
        document.body.appendChild(f);
        await new Promise((r) => (f.onload = r));
        // What ExtensionHostChannel does on each frame load.
        f.contentWindow.postMessage({ type: 'exthello-request' }, '*');
        const deadline = Date.now() + 5000;
        while (Date.now() < deadline) {
            const hit = window.__msgs.find((m) => m && m.type === 'exthello');
            if (hit) return hit;
            await new Promise((r) => setTimeout(r, 100));
        }
        return null;
    }, { ext: EXT, token: TOKEN })
    .catch((e) => ({ error: String(e) }));

console.log('bridge hello from the frame:', JSON.stringify(hello));
console.log(
    hello && hello.token === TOKEN
        ? `PASS — extHost loaded in an opaque-origin frame and echoed the mount token`
        : 'FAIL — the extension document never handshook (bridge not loaded?)',
);
await browser.close();
