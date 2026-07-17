import { chromium } from 'playwright';

const browser = await chromium.launch();
const page = await browser.newPage();

const events = [];

page.on('console', msg => {
    if (msg.text().includes('[ws-debug]')) {
        events.push(msg.text());
        console.log(msg.text());
    }
});

await page.goto('http://localhost:3000/');
await page.waitForSelector('.card', { timeout: 10000 });

console.log('Page loaded, getting project ID...');
const projectId = await page.evaluate(async () => {
    const res = await fetch('/api/projects');
    const projects = await res.json();
    return projects[0]?.id || '';
});
console.log('Project ID:', projectId);

// Get CSRF token
const csrfToken = await page.evaluate(async () => {
    const res = await fetch('/api/auth/session');
    const data = await res.json();
    return data.csrf_token || '';
});

// Move a task to in_progress
const firstTaskId = await page.evaluate(async (pid) => {
    const res = await fetch(`/api/projects/${pid}/board`);
    const board = await res.json();
    return board.tasks[0]?.id || '';
}, projectId);

console.log('First task ID:', firstTaskId);

// Navigate to the task
await page.evaluate((tid) => {
    window.location.href = `/?task=${tid}`;
}, firstTaskId);

await page.waitForSelector('.task-detail-page', { timeout: 10000 });
console.log('CardDetail opened');

// Wait a bit for WebSocket to connect
await page.waitForTimeout(2000);

// Now stop the task via API
console.log('Stopping task via API...');
const stopResult = await page.evaluate(async ({ csrf, pid, tid }) => {
    const res = await fetch(`/api/projects/${pid}/tasks/${tid}/stop`, {
        method: 'POST',
        headers: {
            'Content-Type': 'application/json',
            'X-CSRF-Token': csrf,
        },
        credentials: 'include',
    });
    return res.ok;
}, { csrf: csrfToken, pid: projectId, tid: firstTaskId });

console.log('Stop API result:', stopResult);

// Wait for WebSocket event
await page.waitForTimeout(5000);

// Check the badge
const badgeText = await page.evaluate(() => {
    const badge = document.querySelector('.task-detail-page .badge');
    return badge ? badge.textContent : 'NO BADGE';
});
console.log('Badge text after stop:', badgeText);

console.log('\n--- All WS debug events ---');
events.forEach((e, i) => console.log(`  ${i}: ${e}`));

await browser.close();
