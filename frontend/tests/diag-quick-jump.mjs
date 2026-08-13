// Verify: shortly after opening a large Go file, a big jump lands on parsed
// content (full-document parse drive works). No 10s wait.
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

// Wait only 800ms after open (vs 10s before), then jump around and check spans
await page.waitForTimeout(800);

const read = () => page.evaluate(() => {
  const spans = document.querySelectorAll('.cm-content span[class]').length;
  const scroller = document.querySelector('.cm-scroller');
  return { spans, scrollTop: scroller?.scrollTop };
});

for (const frac of [0.5, 0.75, 0.9, 0.3, 0.8, 0.6, 0.99, 0.1]) {
  await page.evaluate((f) => {
    const el = document.querySelector('.cm-scroller');
    if (el) el.scrollTop = f * (el.scrollHeight - el.clientHeight);
  }, frac);
  const immediate = await read();
  await page.waitForTimeout(40);
  const after40 = await read();
  const ok = immediate.spans > 0 && after40.spans > 0;
  console.log(`jump ${frac}: immediate=${JSON.stringify(immediate)} after40=${JSON.stringify(after40)} ${ok ? 'OK' : '!!! WASHOUT'}`);
}

await browser.close();
