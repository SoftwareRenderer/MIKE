// Measure: during no-span windows, what color is the text? + flicker frequency baseline
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
await page.waitForTimeout(3000);

// Fast scroll; count windows with 0 spans and sample text color then
let zeroWindows = 0;
let samples = [];
for (let round = 0; round < 400; round++) {
  await page.evaluate(() => {
    const el = document.querySelector('.cm-scroller');
    if (!el) return;
    el.scrollTop = Math.floor(Math.random() * (el.scrollHeight - el.clientHeight));
  });
  await page.waitForTimeout(10);
  const s = await page.evaluate(() => {
    const spans = document.querySelectorAll('.cm-content span[class]').length;
    const line = document.querySelector('.cm-content .cm-line');
    const textNode = line ? [...line.childNodes].find(n => n.nodeType === 3) : null;
    return {
      spans,
      textColor: textNode ? getComputedStyle(line).color : null,
      lineClass: line ? line.className : null,
      lineStyle: line ? line.getAttribute('style') : null,
    };
  });
  if (s.spans === 0) {
    zeroWindows++;
    if (samples.length < 5) samples.push({ round, ...s });
  }
}
console.log('zeroWindows (out of 400):', zeroWindows);
console.log('samples:', JSON.stringify(samples, null, 1));
await browser.close();
