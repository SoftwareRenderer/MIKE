// Large Go file scroll stress test — watch for syntax highlighting loss / low contrast
import { chromium } from 'playwright';

const BASE = 'http://localhost:3000';
const loginRes = await fetch(`${BASE}/api/auth/login`, {
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ password: 'test-password-1234' }),
});
const setCookie = loginRes.headers.get('set-cookie');
const cookie = setCookie.split(';')[0];

const browser = await chromium.launch({ headless: true });
const ctx = await browser.newContext({ viewport: { width: 1400, height: 900 } });
await ctx.addCookies([{ name: cookie.split('=')[0], value: cookie.split('=')[1], domain: 'localhost', path: '/', httpOnly: true }]);
const page = await ctx.newPage();
page.on('pageerror', (e) => console.log('PAGE ERROR:', String(e).slice(0, 300)));
page.on('console', (m) => { if (m.type() === 'error') console.log('CONSOLE ERR:', m.text().slice(0, 300)); });

await page.goto(BASE + '/');
await page.waitForSelector('.project-item', { timeout: 15000 });
await page.locator('.project-item').first().click();
await page.waitForSelector('.top-bar, .board', { timeout: 15000 });
await page.locator('.explorer-btn').click().catch(() => {});
await page.waitForSelector('.file-browser-list', { timeout: 15000 });
const pathInput = page.locator('.file-browser-path-input');
await pathInput.fill('/tmp/repro-repo');
await pathInput.press('Enter');
await page.waitForTimeout(800);
await page.locator('.file-browser-item', { hasText: 'big.go' }).first().click();
await page.waitForSelector('.cm-editor', { timeout: 15000 });
await page.waitForTimeout(3000);

async function probe(tag, screenshot) {
  const s = await page.evaluate(() => {
    const content = document.querySelector('.cm-content');
    const spans = content ? [...content.querySelectorAll('span[class]')] : [];
    const colors = [...new Set(spans.map(s => getComputedStyle(s).color))];
    const sticky = document.querySelector('.cm-sticky-scroll');
    const editor = document.querySelector('.cm-editor');
    // Count spans whose computed color is dim (low contrast vs #0d1117)
    const dim = colors.filter(c => {
      const m = /rgb\((\d+), (\d+), (\d+)\)/.exec(c);
      if (!m) return false;
      const [r, g, b] = [parseInt(m[1]), parseInt(m[2]), parseInt(m[3])];
      return (r + g + b) / 3 < 120;
    });
    return {
      lineCount: content ? content.querySelectorAll('.cm-line').length : 0,
      spanCount: spans.length,
      colorCount: colors.length,
      colors: colors.slice(0, 10),
      dimColors: dim,
      editorColor: editor ? getComputedStyle(editor).color : null,
      stickyDisplay: sticky ? getComputedStyle(sticky).display : 'none',
      stickyText: sticky ? (sticky.textContent || '').slice(0, 40) : '',
      scrollTop: document.querySelector('.cm-scroller')?.scrollTop,
    };
  });
  console.log(`${tag}:`, JSON.stringify(s));
  if (screenshot) await page.screenshot({ path: `/tmp/go-stress-${tag}.png` });
  return s;
}

await probe('initial', true);

// Fast momentum-style jumps across the whole file
let sawIssue = false;
for (let i = 0; i < 80; i++) {
  await page.evaluate(() => {
    const el = document.querySelector('.cm-scroller');
    if (!el) return;
    el.scrollTop = Math.floor(Math.random() * (el.scrollHeight - el.clientHeight));
  });
  await page.waitForTimeout(15);
  if (i % 10 === 0) {
    const s = await probe('mid-' + i, false);
    if (s.spanCount === 0 || s.colorCount <= 2 || s.dimColors.length >= s.colorCount) {
      sawIssue = true;
      console.log('!!! POSSIBLE ISSUE at iter', i);
      await page.screenshot({ path: `/tmp/go-stress-issue-${i}.png` });
    }
  }
}

// Now scroll smoothly line by line through a section
for (let i = 0; i < 120; i++) {
  await page.evaluate(() => {
    const el = document.querySelector('.cm-scroller');
    if (el) el.scrollTop += 24;
  });
  await page.waitForTimeout(12);
}
await probe('smooth-end', true);

console.log('sawIssue:', sawIssue);
await browser.close();
