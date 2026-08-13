// A/B: incremental momentum-style scrolling — count frames where syntax highlighting vanishes
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
await page.waitForTimeout(4000); // let the file fully parse first

// Incremental fast scroll: big steps like trackpad momentum, wrapping
let zero = 0;
let total = 0;
for (let i = 0; i < 600; i++) {
  await page.evaluate(() => {
    const el = document.querySelector('.cm-scroller');
    if (!el) return;
    let t = el.scrollTop + 450;
    const max = el.scrollHeight - el.clientHeight;
    if (t >= max) t = 0;
    el.scrollTop = t;
  });
  await page.waitForTimeout(8);
  const s = await page.evaluate(() => document.querySelectorAll('.cm-content span[class]').length);
  total++;
  if (s === 0) zero++;
}
console.log(`incremental scroll: ${zero} zero-span frames out of ${total} (${(zero / total * 100).toFixed(1)}%)`);
await browser.close();
