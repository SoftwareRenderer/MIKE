// Controlled: fully parse (wait), then single large jump — check spans at the new viewport immediately.
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
await page.waitForTimeout(10000); // let full background parse complete

const read = () => page.evaluate(() => {
  const spans = document.querySelectorAll('.cm-content span[class]').length;
  const scroller = document.querySelector('.cm-scroller');
  return { spans, scrollTop: scroller?.scrollTop, scrollHeight: scroller?.scrollHeight, clientHeight: scroller?.clientHeight };
});

console.log('initial (fully parsed):', JSON.stringify(await read()));

// Single jump to various positions, checking immediately and after 50ms/250ms
const heights = [0.25, 0.5, 0.75, 0.9, 0.3, 0.6, 0.15];
for (const frac of heights) {
  await page.evaluate((f) => {
    const el = document.querySelector('.cm-scroller');
    if (el) el.scrollTop = f * (el.scrollHeight - el.clientHeight);
  }, frac);
  const immediate = await read();
  await page.waitForTimeout(50);
  const after50 = await read();
  await page.waitForTimeout(250);
  const after300 = await read();
  console.log(`jump to ${frac}: immediate=${JSON.stringify(immediate)} after50=${JSON.stringify(after50)} after300=${JSON.stringify(after300)}`);
}

await browser.close();
