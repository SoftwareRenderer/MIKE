// Live check of the paste intake against a running dev server:
//
//   make run
//   curl -s -c /tmp/p.jar -D- -o /dev/null -X POST localhost:3000/api/auth/login \
//        -H 'Content-Type: application/json' -d "{\"password\":\"$MIKE_LOGIN_PASSWORD\"}" \
//     | grep -i x-csrf-token          # → X-CSRF-Token for the calls below
//   POST /api/projects                     {"name":"paste-probe","path":"<a git repo>","sandbox_enabled":false}
//   POST /api/projects/<p>/tasks           {"title":"paste probe","prompt":"probe"}
//   PUT  /api/projects/<p>/tasks/<t>       {"status":"in_progress"}
//   MIKE_LOGIN_PASSWORD=… PROJECT=<p> TASK=<t> node tests/paste-intake-check.mjs
//
// Needs a task past backlog (the backlog hero replaces the chat input). What jsdom cannot answer:
// whether the event the app listens for is the one a real paste produces, and whether the file
// bucket's round trip to POST …/uploads inserts a path in the field.
import { chromium } from 'playwright';

const BASE = process.env.BASE || 'http://localhost:3000';
const PROJECT = process.env.PROJECT || 'kanban-upload-probe';
const TASK = process.env.TASK || 'wander-oath-violet';
const PASSWORD = process.env.MIKE_LOGIN_PASSWORD;
if (!PASSWORD) throw new Error('MIKE_LOGIN_PASSWORD is not set');

const browser = await chromium.launch();
const ctx = await browser.newContext();
const page = await ctx.newPage();
page.on('pageerror', (e) => console.log('[pageerror]', e.message));
page.on('response', (r) => {
    if (r.url().includes('/uploads')) console.log(`[resp:${r.status()}]`, r.url());
});

await page.goto(BASE + '/', { waitUntil: 'commit' });
await page.fill('#login-password', PASSWORD);
await page.keyboard.press('Enter');

await page.goto(`${BASE}/?project=${encodeURIComponent(PROJECT)}&task=${encodeURIComponent(TASK)}`, {
    waitUntil: 'commit',
});

const textarea = page.locator('#chat-message-input');
await textarea.waitFor({ state: 'visible', timeout: 15000 });

const result = await page.evaluate(async () => {
    const el = document.querySelector('#chat-message-input');
    const dt = new DataTransfer();
    dt.items.add(new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], 'shot.png', { type: 'image/png' }));
    dt.items.add(new File(['build failed at step 3\n'], 'notes.txt', { type: 'text/plain' }));
    dt.items.add(new File([new Uint8Array([0x50, 0x4b, 0x03, 0x04, 0x00, 0xff])], 'bundle.zip', { type: 'application/zip' }));

    el.focus();
    el.dispatchEvent(new ClipboardEvent('paste', { bubbles: true, cancelable: true, clipboardData: dt }));
    // The file bucket is an upload: wait for the round trip rather than guessing a duration.
    for (let i = 0; i < 60; i++) {
        if (el.value.includes('/uploads/') || el.value.includes('kanban-uploads')) break;
        await new Promise((r) => setTimeout(r, 100));
    }
    await new Promise((r) => setTimeout(r, 300));
    return {
        thumbs: document.querySelectorAll('.attachment-thumb').length,
        value: el.value,
    };
});

const checks = {
    'the image became an attachment': result.thumbs === 1,
    'the text document was inlined as a fenced block': /Pasted notes\.txt[\s\S]*build failed at step 3/.test(result.value),
    'the archive was written and named by path': /Uploaded bundle\.zip to \/\S+bundle\.zip/.test(result.value),
};
console.log('attachment thumbs:', result.thumbs);
console.log('field content:\n' + result.value);
for (const [what, ok] of Object.entries(checks)) console.log(`${ok ? 'PASS' : 'FAIL'} — ${what}`);
console.log(
    Object.values(checks).every(Boolean)
        ? 'PASS — one paste, three buckets, one field'
        : 'FAIL — see the field content above',
);

await browser.close();
