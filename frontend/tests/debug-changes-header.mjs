import { chromium } from 'playwright';

const BASE = 'https://localhost:3000';

const browser = await chromium.launch();
const context = await browser.newContext({ ignoreHTTPSErrors: true });
const page = await context.newPage();

// Login via API (session cookie lands on the shared context)
const res = await context.request.post(`${BASE}/api/auth/login`, { data: { password: 'test' } });
console.log('login:', res.status());

await page.goto(BASE + '/');
await page.waitForSelector('.project-item', { timeout: 15000 });
await page.locator('.project-item').first().click();
await page.waitForSelector('.top-bar', { timeout: 15000 });
await page.locator('.explorer-btn').click();
await page.waitForSelector('.explorer-view', { timeout: 15000 });
await page.locator('.git-status-btn').click();
await page.waitForSelector('.panel-title:has-text("Changes")', { timeout: 15000 });
await page.waitForTimeout(1200); // let data load + layout settle

// Measure the horizontal gaps in the header for the report
const gaps = await page.evaluate(() => {
    const header = document.querySelector('.panel-header');
    const items = [...header.children].map(el => ({
        cls: el.className.split(' ').slice(0, 2).join('.'),
        left: Math.round(el.getBoundingClientRect().left),
        right: Math.round(el.getBoundingClientRect().right),
    }));
    const out = [];
    for (let i = 1; i < items.length; i++) {
        out.push(`${items[i - 1].cls} -> ${items[i].cls}: ${items[i].left - items[i - 1].right}px`);
    }
    return { headerWidth: Math.round(header.getBoundingClientRect().width), gaps: out };
});
console.log(JSON.stringify(gaps, null, 2));

await page.screenshot({ path: '/tmp/review-changes-header.png' });
await browser.close();
console.log('screenshot: /tmp/review-changes-header.png');
