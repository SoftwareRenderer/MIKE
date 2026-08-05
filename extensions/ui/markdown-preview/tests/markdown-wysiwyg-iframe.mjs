/**
 * Iframe-level integration test for the markdown-preview WYSIWYG extension.
 *
 * Mounts the real extension index.html (with the real host-client.js, the
 * Wordgard bundle and serializer) in a sandboxed iframe on a page served from
 * the dev server, then acts as the host over the postMessage capability
 * protocol:
 *   - init → extension adds a Preview button
 *   - forward editor.footerButtonClick → extension toggles preview, calls
 *     editor.getContent (we return markdown), and mounts a Wordgard editor
 *   - type in the Wordgard editor → extension calls editor.setContent; we
 *     capture and verify it contains the typed edit
 *
 * Requires a dev server with TLS on the configured base URL.
 * Run: cd extensions/ui/markdown-preview && node tests/markdown-wysiwyg-iframe.mjs
 */
import { createRequire } from 'node:module';
import { resolve } from 'node:path';

// Reuse the Playwright dev dependency from the frontend workspace.
const dir = new URL('.', import.meta.url).pathname;
const require = createRequire(resolve(dir, '..', '..', '..', '..', 'frontend', 'package.json'));
const { chromium } = require('playwright');

const BASE = process.env.MARKDOWN_PREVIEW_BASE || 'https://localhost:3000';
const MD = '# Hello WYSIWYG\n\nSome *markdown* here.\n';

const browser = await chromium.launch();
const context = await browser.newContext({ ignoreHTTPSErrors: true });
const page = await context.newPage();
const errors = [];
page.on('pageerror', e => errors.push('pageerror: ' + e));
page.on('console', m => { if (m.type() === 'error') errors.push('console: ' + m.text()); });

const setContentCalls = [];
const calls = [];

async function waitUntil(fn, timeout = 10000) {
    const t0 = Date.now();
    while (Date.now() - t0 < timeout) {
        if (fn()) return true;
        await new Promise(r => setTimeout(r, 200));
    }
    return false;
}

let ok = true;
try {
    // Serve the extension from the dev origin so its /host-client.js and the
    // wordgard bundle resolve. Replace the body with our harness + the iframe.
    await page.goto(`${BASE}/`, { waitUntil: 'domcontentloaded' });
    await page.setContent(`
      <div id="host"></div>
      <iframe id="ext" sandbox="allow-scripts" src="${BASE}/extensions/ui/markdown-preview/index.html"
        style="width:600px;height:400px;border:none"></iframe>
    `);

    // Intercept messages the extension posts to us (its parent).
    await page.exposeFunction('__onCall', (msg) => {
        calls.push(msg);
        switch (msg.cap) {
            case 'editor.getContent': return { content: MD };
            case 'editor.setContent': setContentCalls.push(msg.args.content); return {};
            default: return {};
        }
    });
    await page.evaluate(() => {
        window.addEventListener('message', async (ev) => {
            if (!ev.data || ev.data.type !== 'call') return;
            const result = await window.__onCall(ev.data);
            for (const f of document.querySelectorAll('iframe')) {
                if (f.contentWindow === ev.source) {
                    f.contentWindow.postMessage({ type: 'response', id: ev.data.id, ok: true, data: result }, '*');
                }
            }
        });
    });

    const frame = page.frames().find(f => f.url().includes('markdown-preview'));
    if (!frame) throw new Error('extension iframe did not load');

    // Send init so the extension activates (filePath .md → markdown extension).
    page.evaluate(() => {
        const f = document.getElementById('ext');
        f.contentWindow.postMessage({
            type: 'init',
            extensionId: 'markdown-preview', extensionName: 'markdown-preview', point: 'fileEditor',
            granted: ['editor.addFooterButton','editor.getContent','editor.setContent','editor.setOverlay','editor.contentChange','config.get','log.write'],
            config: {},
            context: { component: 'FileEditor', filePath: 'README.md' },
        }, '*');
    });

    // The extension should try to add its Preview footer button.
    if (!(await waitUntil(() => calls.some(c => c.cap === 'editor.addFooterButton')))) { ok = false; throw new Error('no editor.addFooterButton call'); }
    console.log('OK  extension registered Preview button (editor.addFooterButton called)');

    // Simulate a footer button click → toggles preview on.
    page.evaluate(() => {
        const f = document.getElementById('ext');
        f.contentWindow.postMessage({ type: 'event', cap: 'editor.footerButtonClick', data: { id: 'markdown-preview' } }, '*');
    });

    // The extension should fetch content and mount a Wordgard editor.
    const wg = frame.locator('wordgard-editor').first();
    await wg.waitFor({ state: 'visible', timeout: 20000 });
    console.log('OK  Wordgard editor mounted in preview iframe');
    await waitUntil(() => calls.some(c => c.cap === 'editor.getContent'));

    // The formatting toolbar (menu bar) should be mounted alongside the editor.
    const menuBar = frame.locator('wg-menubar');
    await menuBar.waitFor({ state: 'attached', timeout: 10000 });
    const btnCount = await menuBar.locator('button.wg-menu-button').count();
    if (btnCount > 0) { console.log(`OK  menu bar mounted with ${btnCount} toolbar buttons`); }
    else { ok = false; console.error('FAIL menu bar mounted but has no toolbar buttons'); }

    // Type into the Wordgard editor.
    await frame.locator('wg-content').click();
    await page.keyboard.press('End');
    await page.keyboard.type(' EDITED');
    await page.waitForTimeout(700);

    if (setContentCalls.length === 0) {
        ok = false; console.error('FAIL editor.setContent was never called after typing');
    } else {
        const last = setContentCalls[setContentCalls.length - 1];
        console.log('editor.setContent payload:', JSON.stringify(last));
        if (last.includes('EDITED')) { console.log('OK  WYSIWYG edit pushed to host via editor.setContent'); }
        else { ok = false; console.error('FAIL setContent payload missing the edit'); }
    }
} catch (e) {
    ok = false;
    console.error('iframe test error:', e && e.message ? e.message : e);
    await page.screenshot({ path: '/tmp/wysiwyg-iframe.png' }).catch(() => {});
} finally {
    console.log('calls received:', calls.map(c => c.cap).join(', ') || '(none)');
    console.log('page errors:', errors.length ? errors : 'none');
    await browser.close();
}
process.exit(ok ? 0 : 1);
