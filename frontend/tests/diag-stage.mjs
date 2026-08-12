// Diagnostic: verify staging an untracked file moves it to the Staged section in
// the diff panel via the backend broadcast (no page reload).
// Run against a live dev/test server.
import { chromium } from 'playwright';
import { execSync } from 'child_process';
import { writeFileSync } from 'fs';
import { join } from 'path';

const BASE = 'http://localhost:3000';
const PASSWORD = 'test-password-1234';

async function main() {
    // Find a task with a worktree via the API (login first).
    const cookieJar = '/tmp/cj-diag.txt';
    execSync(`curl -s -c ${cookieJar} -X POST ${BASE}/api/auth/login -H 'Content-Type: application/json' -d '{"password":"${PASSWORD}"}' > /dev/null`);
    const cookie = execSync(`cat ${cookieJar}`).toString();
    const phpsessid = cookie.match(/PHPSESSID\s+(\S+)/)?.[1];
    if (!phpsessid) { console.error('login failed'); process.exit(1); }

    const projects = JSON.parse(execSync(`curl -s -b ${cookieJar} ${BASE}/api/projects`).toString());
    const project = projects[0];
    const tasks = JSON.parse(execSync(`curl -s -b ${cookieJar} ${BASE}/api/projects/${project.id}/tasks`).toString());
    const task = tasks.find(t => t.branch) || tasks[0];
    console.log('using project', project.id, 'task', task.id, 'status', task.status);

    const browser = await chromium.launch();
    const context = await browser.newContext();
    await context.addCookies([{ name: 'PHPSESSID', value: phpsessid, domain: 'localhost', path: '/', httpOnly: true, secure: false, sameSite: 'Lax' }]);
    const page = await context.newPage();
    page.on('console', msg => {
        const t = msg.text();
        if (t.includes('[diff]') || t.includes('diff') || t.includes('error')) console.log('[console]', t.slice(0, 300));
    });
    page.on('pageerror', e => console.log('[pageerror]', String(e).slice(0, 300)));
    // Capture WebSocket frames to observe diff_stats broadcasts.
    page.on('websocket', ws => {
        ws.on('framereceived', e => {
            const s = String(e.payload);
            if (s.includes('diff_stats') || s.includes('diff')) console.log('[ws-recv]', s.slice(0, 800));
        });
    });

    // Locate the task's worktree on disk (env var may differ from config value).
    const candidates = ['/tmp/kanban-test-worktrees', '/home/sandbox/MIKE/.worktrees'];
    let worktreePath = null;
    for (const base of candidates) {
        const p = join(base, project.id, task.id);
        try { execSync(`test -e ${p}/.git`); worktreePath = p; break; } catch { /* next */ }
    }
    if (!worktreePath) { console.error('worktree not found'); process.exit(1); }
    const testFile = `diag-stage-${Date.now()}.txt`;
    writeFileSync(join(worktreePath, testFile), 'untracked content line\n');

    await page.goto(`${BASE}/?project=${project.id}&task=${task.id}`);
    await page.waitForSelector('.task-detail-page', { timeout: 20000 });
    console.log('task detail visible');

    // Open Changes tab if needed.
    const diffColumn = page.locator('.column-diff');
    if (!await diffColumn.isVisible().catch(() => false)) {
        const changesTab = page.locator('.tab-btn:has-text("Changes")');
        if (await changesTab.isVisible().catch(() => false)) await changesTab.click();
    }
    await page.waitForSelector('.diff-content', { timeout: 20000 });
    console.log('diff content visible');

    // Wait for the file to appear in the panel.
    await page.waitForFunction((f) => document.querySelector('.diff-scroll')?.textContent?.includes(f), testFile, { timeout: 20000 });
    console.log('file visible in panel');

    const unstagedSection = page.locator('.diff-section').filter({ has: page.locator('.diff-section-header.diff-section-unstaged') });
    const stagedSection = page.locator('.diff-section').filter({ has: page.locator('.diff-section-header.diff-section-staged') });

    const inUnstaged = (await unstagedSection.textContent().catch(() => ''))?.includes(testFile) ?? false;
    console.log('before stage: in Unstaged section =', inUnstaged);

    // Click the stage button on the file.
    const fileSection = page.locator('.diff-file-section', { hasText: testFile }).first();
    await fileSection.locator('.diff-stage-btn').click();
    console.log('clicked stage button');

    // Wait for the file to move to the Staged section (requires the broadcast).
    let moved = false;
    for (let i = 0; i < 30; i++) {
        const txt = await stagedSection.textContent().catch(() => '');
        if (txt?.includes(testFile)) { moved = true; break; }
        await new Promise(r => setTimeout(r, 500));
    }
    console.log('after stage: in Staged section =', moved);
    console.log(moved ? 'PASS: broadcast moved file to Staged section' : 'FAIL: file did not move to Staged section');

    await page.screenshot({ path: '/tmp/diag-stage.png' });
    await browser.close();
    execSync(`rm -f ${join(worktreePath, testFile)}`);
    process.exit(moved ? 0 : 1);
}

main().catch(e => { console.error('ERROR:', e); process.exit(2); });
