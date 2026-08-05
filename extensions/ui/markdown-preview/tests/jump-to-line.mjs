/**
 * Iframe-level integration test for markdown-preview's symbol-outline jump.
 *
 * Verifies that with the WYSIWYG editor open, when the host forwards an
 * `editor.jumpToLine` event (which the Outline panel's symbol click triggers
 * via the code editor's `jump-to-line`), the Wordgard editor scrolls to the
 * heading at that markdown source line.
 *
 * The document is long enough that the later headings start below the fold, so
 * we can assert scrolling by comparing the target heading's bounding rect
 * before and after the jump.
 *
 * Requires a dev server with TLS on the configured base URL.
 * Run: cd extensions/ui/markdown-preview && node tests/jump-to-line.mjs
 */
import { createRequire } from 'node:module';
import { resolve } from 'node:path';

// Reuse the Playwright dev dependency from the frontend workspace.
const dir = new URL('.', import.meta.url).pathname;
const require = createRequire(resolve(dir, '..', '..', '..', '..', 'frontend', 'package.json'));
const { chromium } = require('playwright');

const BASE = process.env.MARKDOWN_PREVIEW_BASE || 'https://localhost:3000';

// Headings: `# Alpha`, `## Beta`, `### Gamma`.
// Fenced bash code blocks with `#` comments sit between the sections — these
// comments must NOT be treated as headings (regression: outline jumps used to
// count them and land on the wrong heading).
// Paragraphs are long enough that the later headings start well below the fold.
const FILLER = 'This is a reasonably long paragraph of body text that keeps the rendered section tall so the headings further down the document sit well below the fold of the preview viewport. It repeats across the sections to force meaningful scrolling.';
function section(title, lines) {
    return [title, ''].concat(lines).concat(['']);
}
const CODE_BLOCK = [
    '```bash',
    '# Build for Linux (CGO_ENABLED=0 for static build)',
    'CGO_ENABLED=0 go build -trimpath -o kanban .',
    '# another bash comment that is not a heading',
    '```',
    '',
];
const MD = [
    ...section('# Alpha', [FILLER, '', FILLER, '', FILLER, '']),
    ...CODE_BLOCK,
    ...section('## Beta', [FILLER, '', FILLER, '', FILLER, '']),
    ...CODE_BLOCK,
    ...section('### Gamma', [FILLER, '', FILLER, '', FILLER, '']),
].join('\n');

// 1-based markdown source line of each heading (used as the jump target).
function headingLine(prefix) {
    return MD.split('\n').findIndex(l => l === prefix) + 1;
}
const GAMMA_LINE = headingLine('### Gamma');
const BETA_LINE = headingLine('## Beta');

const browser = await chromium.launch();
const context = await browser.newContext({ ignoreHTTPSErrors: true });
const page = await context.newPage();
const errors = [];
page.on('pageerror', e => errors.push('pageerror: ' + e));
page.on('console', m => { if (m.type() === 'error') errors.push('console: ' + m.text()); });

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
    await page.goto(`${BASE}/`, { waitUntil: 'domcontentloaded' });
    await page.setContent(`
      <div id="host"></div>
      <iframe id="ext" sandbox="allow-scripts" src="${BASE}/extensions/ui/markdown-preview/index.html"
        style="width:600px;height:400px;border:none"></iframe>
    `);

    await page.exposeFunction('__onCall', (msg) => {
        calls.push(msg.cap);
        switch (msg.cap) {
            case 'editor.getContent': return { content: MD };
            case 'editor.setContent': return {};
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

    // Activate the extension (filePath .md → markdown extension).
    page.evaluate(() => {
        document.getElementById('ext').contentWindow.postMessage({
            type: 'init',
            extensionId: 'markdown-preview', extensionName: 'markdown-preview', point: 'fileEditor',
            granted: ['editor.addFooterButton','editor.getContent','editor.setContent','editor.setOverlay','editor.contentChange','editor.jumpToLine','config.get','log.write'],
            config: {},
            context: { component: 'FileEditor', filePath: 'README.md' },
        }, '*');
    });

    // Toggle the WYSIWYG preview on.
    page.evaluate(() => {
        document.getElementById('ext').contentWindow.postMessage({
            type: 'event', cap: 'editor.footerButtonClick', data: { id: 'markdown-preview' },
        }, '*');
    });
    await frame.locator('wordgard-editor').first().waitFor({ state: 'visible', timeout: 20000 });
    await waitUntil(() => calls.includes('editor.getContent'));
    console.log('OK  WYSIWYG editor mounted');

    // The document should be taller than the editor's scroller so scrolling is
    // observable. The scroll container is the `wg-scroller` element (set up by
    // Wordgard.scrolling('100%')), not the outer wordgard-editor.
    const overflow = await frame.evaluate(() => {
        const s = document.querySelector('wg-scroller');
        return s.scrollHeight > s.clientHeight;
    });
    if (!overflow) { ok = false; throw new Error('document does not overflow the editor scroller — test not meaningful'); }
    console.log('OK  document overflows editor scroller (scrolling testable)');

    // Record the Gamma heading's position before the jump (in the scroller).
    const gammaBefore = await frame.evaluate(() => {
        const s = document.querySelector('wg-scroller');
        const h = s.querySelector('h3');
        return h.getBoundingClientRect().top - s.getBoundingClientRect().top;
    });
    console.log(`Gamma heading top (in scroller) before jump: ${gammaBefore.toFixed(1)}px`);
    const scrollerH = await frame.evaluate(() => document.querySelector('wg-scroller').clientHeight);
    if (gammaBefore < scrollerH * 0.5) { ok = false; throw new Error('Gamma heading already near top before jump — test not meaningful'); }

    // Simulate clicking the Gamma reference in the Outline panel: the host
    // dispatches jump-to-line, which FileEditor forwards to the extension.
    page.evaluate((line) => {
        document.getElementById('ext').contentWindow.postMessage({
            type: 'event', cap: 'editor.jumpToLine', data: { line },
        }, '*');
    }, GAMMA_LINE);

    // Give the extension time to fetch content and scroll.
    await page.waitForTimeout(600);
    const gammaAfter = await frame.evaluate(() => {
        const s = document.querySelector('wg-scroller');
        const h = s.querySelector('h3');
        return h.getBoundingClientRect().top - s.getBoundingClientRect().top;
    });
    console.log(`Gamma heading top (in scroller) after jump:  ${gammaAfter.toFixed(1)}px`);

    if (gammaAfter < gammaBefore - 1 && gammaAfter < 100) {
        console.log('OK  editor scrolled Gamma heading to the top of the viewport');
    } else {
        ok = false;
        console.error(`FAIL expected Gamma heading near viewport top, got top=${gammaAfter.toFixed(1)}px (before=${gammaBefore.toFixed(1)}px)`);
    }

    // Also verify the jump is scoped to the target heading: jumping to Beta
    // should leave Gamma below it again (the two headings don't collide).
    page.evaluate((line) => {
        document.getElementById('ext').contentWindow.postMessage({
            type: 'event', cap: 'editor.jumpToLine', data: { line },
        }, '*');
    }, BETA_LINE);
    await page.waitForTimeout(600);
    const betaTop = await frame.evaluate(() => {
        const s = document.querySelector('wg-scroller');
        const h = s.querySelector('h2');
        return h.getBoundingClientRect().top - s.getBoundingClientRect().top;
    });
    console.log(`Beta heading top (in scroller) after second jump: ${betaTop.toFixed(1)}px`);
    if (betaTop < 100) {
        console.log('OK  second jump scrolled to Beta heading');
    } else {
        ok = false;
        console.error(`FAIL expected Beta heading near viewport top, got top=${betaTop.toFixed(1)}px`);
    }
} catch (e) {
    ok = false;
    console.error('jump-to-line test error:', e && e.message ? e.message : e);
    await page.screenshot({ path: '/tmp/jump-to-line.png' }).catch(() => {});
} finally {
    console.log('calls received:', calls.join(', ') || '(none)');
    console.log('page errors:', errors.length ? errors : 'none');
    await browser.close();
}
process.exit(ok ? 0 : 1);
