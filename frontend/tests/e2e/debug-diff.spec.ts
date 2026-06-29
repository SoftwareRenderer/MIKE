import { test, expect } from '@playwright/test';
import { execSync } from 'child_process';

test('debug diff auto-update', async ({ page }) => {
    const logs: string[] = [];
    const requests: { time: number; url: string }[] = [];

    page.on('console', msg => {
        const text = msg.text();
        if (text.includes('diff') || text.includes('card') || text.includes('CardDetail')) {
            logs.push(text);
        }
    });
    page.on('request', req => {
        const url = req.url();
        if (url.includes('/diff') || url.includes('/commits')) {
            requests.push({ time: Date.now(), url });
        }
    });

    // Login
    await page.goto('http://localhost:3000/');
    await page.waitForTimeout(500);
    await page.fill('input[type="password"]', 'test');
    await page.press('input[type="password"]', 'Enter');
    await page.waitForTimeout(2000);

    // Navigate to task with review status
    await page.goto('http://localhost:3000/?project=temp&task=power-iron-shadow');
    await page.waitForTimeout(3000);

    // Wait for initial load
    await page.waitForTimeout(2000);

    console.log('=== Initial state ===');
    console.log('Logs:', logs.join('\n'));
    console.log('Requests:', requests.length);

    // Modify a file in the worktree
    const taskId = 'power-iron-shadow';
    const worktreePath = `/home/mwren/projects/kanban/worktrees/${taskId}`;
    try {
        execSync(`echo "<!-- test change at \$(date +%s) -->" >> ${worktreePath}/README.md`);
        console.log('File modified');
    } catch (e) {
        console.log('Failed to modify file:', e);
    }

    // Wait for watcher debounce (2s) + processing
    await page.waitForTimeout(5000);

    console.log('\n=== After file change ===');
    console.log('New logs:', logs.slice(-10).join('\n'));
    console.log('Total requests:', requests.length);

    // Check if diff was reloaded
    const recentRequests = requests.filter(r => r.time > Date.now() - 6000);
    console.log('Recent requests (6s):', recentRequests.length);
    for (const r of recentRequests) {
        console.log(`  ${r.time}: ${r.url.split('?')[0]}`);
    }
});
