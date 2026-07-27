import { chromium } from 'playwright';

const browser = await chromium.launch();
const context = await browser.newContext({ ignoreHTTPSErrors: true });
const page = await context.newPage();

// Collect network requests for debugging
const diffs = [];
await page.route('**/api/projects/*/tasks/*/diff*', async (route) => {
    const url = route.request().url();
    const response = await route.fetch();
    const body = await response.text();
    try {
        const json = JSON.parse(body);
        diffs.push({ url, hasDiff: json.diff && json.diff.length > 0, diffLen: json.diff?.length || 0, hasCountTxt: json.diff?.includes('count.txt') });
    } catch {
        diffs.push({ url, raw: body.substring(0, 200) });
    }
    await route.fulfill({ response, status: response.status(), headers: response.headers(), body: Buffer.from(body) });
});

await page.goto('https://localhost:3000/', { waitUntil: 'networkidle' });

// Login
await page.evaluate(async () => {
    await fetch('/api/auth/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ password: 'test' })
    });
});
await page.waitForTimeout(500);

// Navigate to the task page
await page.goto('https://localhost:3000/?project=temp&task=frost-rift-vivid', { waitUntil: 'networkidle' });
await page.waitForSelector('.card', { timeout: 10000 });

console.log('\n=== Page loaded ===');

// Look for checkpoint dividers and their "View changes" buttons
const checkpoints = await page.locator('.checkpoint-divider').count();
console.log(`Found ${checkpoints} checkpoint divider(s)`);

for (let i = 0; i < checkpoints; i++) {
    const divider = page.locator('.checkpoint-divider').nth(i);
    const btn = divider.locator('.view-changes-btn');
    const visible = await btn.isVisible();
    console.log(`Checkpoint ${i}: view-changes-btn visible = ${visible}`);
    
    if (visible) {
        // Click the button
        await btn.click();
        console.log(`  -> Clicked "View changes" on checkpoint ${i}`);
        
        // Wait for the overlay
        await page.waitForSelector('.checkpoint-diff-overlay', { timeout: 5000 }).catch(() => null);
        await page.waitForTimeout(1000);
        
        // Check what's shown
        const overlayText = await page.locator('.checkpoint-diff-overlay').textContent().catch(() => 'NOT FOUND');
        console.log(`  -> Overlay text (first 300 chars): ${overlayText.substring(0, 300)}`);
        
        // Check for "No changes" message
        const noChanges = await page.locator('.checkpoint-diff-overlay:has-text("No changes")').count().catch(() => 0);
        console.log(`  -> "No changes" visible: ${noChanges > 0}`);
        
        // Check for count.txt
        const countFile = await page.locator('text=count.txt').count().catch(() => 0);
        console.log(`  -> "count.txt" visible: ${countFile > 0}, count: ${countFile}`);
        
        // Take screenshot
        await page.screenshot({ path: `/tmp/checkpoint-${i}.png` });
        console.log(`  -> Screenshot saved: /tmp/checkpoint-${i}.png`);
        
        // Close the overlay
        const closeBtn = page.locator('.btn-close-checkpoint');
        if (await closeBtn.isVisible()) {
            await closeBtn.click();
            await page.waitForTimeout(500);
        }
    }
}

console.log('\n=== Diff API calls captured ===');
for (const d of diffs) {
    if (d.hasCountTxt !== undefined) {
        console.log(`  ${d.url.split('?')[1] || 'no params'}: diffLen=${d.diffLen}, hasCountTxt=${d.hasCountTxt}`);
    }
}

await context.close();
await browser.close();
