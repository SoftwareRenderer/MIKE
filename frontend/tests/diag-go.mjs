// Go-file specific syntax highlighting probe
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
page.on('pageerror', (e) => console.log('PAGE ERROR:', String(e).slice(0, 400)));
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
await page.locator('.file-browser-item', { hasText: 'sample.go' }).first().click();
await page.waitForSelector('.cm-editor', { timeout: 15000 });
await page.waitForTimeout(2500);

async function probe(tag) {
  const s = await page.evaluate(() => {
    const content = document.querySelector('.cm-content');
    const lineEls = content ? [...content.querySelectorAll('.cm-line')] : [];
    const spans = content ? [...content.querySelectorAll('span[class]')] : [];
    const colors = [...new Set(spans.map(s => getComputedStyle(s).color))];
    const styles = [...new Set(spans.map(s => s.className))];
    // Check whether the cm-content has a specific go-language class or tokens at all
    return {
      lineCount: lineEls.length,
      spanCount: spans.length,
      colorCount: colors.length,
      colors: colors.slice(0, 10),
      sampleClasses: styles.slice(0, 12),
      contentClass: content?.className,
      editorColor: getComputedStyle(document.querySelector('.cm-editor')).color,
      bodyBg: getComputedStyle(document.body).backgroundColor,
      bgPageVar: getComputedStyle(document.documentElement).getPropertyValue('--color-bg-page').trim(),
    };
  });
  console.log(`\n=== ${tag} ===`);
  console.log(JSON.stringify(s, null, 1));
  await page.screenshot({ path: `/tmp/go-${tag}.png` });
}

await probe('initial');

// Scroll through the whole file
for (let i = 0; i < 12; i++) {
  await page.evaluate(() => {
    const el = document.querySelector('.cm-scroller');
    if (el) el.scrollTop += 60;
  });
  await page.waitForTimeout(80);
}
await probe('after-scroll');

await browser.close();
