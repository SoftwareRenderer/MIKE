// Diagnostic: verify .awaiting-llm appears after "Resume Task" (card/API path) —
// regression for stream-handler.ts `status: running` arming awaitingResponse.
// Also verifies the pre-existing arming paths (ReviewBar Resume, normal send).
//
// Run against a live server + mock LLM:
//   1. node frontend/tests/mock-llm.js 9999 &
//   2. MIKE_LOGIN_PASSWORD=test-password-1234 MIKE_TLS=0 DANGER_ZONE=1 \
//      ./kanban --data-dir /tmp/kanban-test-data &
//   3. node frontend/tests/diag-resume.mjs
//
// Exit 0 = PASS, 1 = FAIL (indicator missing), 2 = script error.
import { chromium } from 'playwright';
import { execSync } from 'child_process';
import { readFileSync } from 'fs';

const BASE = 'http://localhost:3000';
const PASSWORD = 'test-password-1234';
const MOCK_URL = 'http://localhost:9999/v1';
const SLOW_WINDOW_MS = 8000; // mock delays first token 3s on "slow-start" prompts

function curl(args) {
    return execSync(`curl -s ${args}`, { encoding: 'utf8', maxBuffer: 16 * 1024 * 1024 });
}

async function pollUntil(fn, timeoutMs, stepMs = 200) {
    const t0 = Date.now();
    for (;;) {
        if (await fn()) return Date.now() - t0;
        if (Date.now() - t0 > timeoutMs) return -1;
        await new Promise(r => setTimeout(r, stepMs));
    }
}

// Resolve the boolean before negating (negating the promise never resolves true).
const visible = (loc) => loc.isVisible().catch(() => false);

async function main() {
    // ── Auth ─────────────────────────────────────────────────────────────────
    const cookieJar = '/tmp/cj-diag-resume.txt';
    const loginHeaders = execSync(
        `curl -s -D - -o /dev/null -c ${cookieJar} -X POST ${BASE}/api/auth/login -H 'Content-Type: application/json' -d '{"password":"${PASSWORD}"}'`,
        { encoding: 'utf8' }
    );
    const csrfToken = (loginHeaders.match(/x-csrf-token:\s*(\S+)/i) || [])[1] || '';
    const cookie = readFileSync(cookieJar, 'utf8');
    const phpsessid = cookie.match(/PHPSESSID\s+(\S+)/)?.[1];
    if (!phpsessid) { console.error('login failed'); process.exit(2); }
    console.log(`[diag] logged in (csrf=${csrfToken ? 'yes' : 'MISSING'})`);

    const getJson = (u) => JSON.parse(curl(`-b ${cookieJar} ${BASE}${u}`));
    const postJson = (u, body) => {
        const out = execSync(`curl -s -b ${cookieJar} -X POST ${BASE}${u} -H 'Content-Type: application/json' ${csrfToken ? `-H 'X-CSRF-Token: ${csrfToken}'` : ''} -d '${JSON.stringify(body).replace(/'/g, "'\\''")}'`, { encoding: 'utf8', maxBuffer: 16 * 1024 * 1024 });
        try { return JSON.parse(out); } catch { return { raw: out }; }
    };
    const putJson = (u, body) => {
        execSync(`curl -s -b ${cookieJar} -X PUT ${BASE}${u} -H 'Content-Type: application/json' ${csrfToken ? `-H 'X-CSRF-Token: ${csrfToken}'` : ''} -d '${JSON.stringify(body).replace(/'/g, "'\\''")}' > /dev/null`, { encoding: 'utf8', maxBuffer: 16 * 1024 * 1024 });
    };

    // ── Mock provider (fresh test server has none) ───────────────────────────
    const config = getJson('/api/config');
    const mockProvider = {
        name: 'mock', display_name: 'Mock LLM', url: MOCK_URL,
        models: [{ name: 'mock-model', reasoning_effort: 'off' }], concurrency: 16,
    };
    putJson('/api/config', {
        ...config, providers: [mockProvider],
        worker_provider: 'mock', worker_model: 'mock-model',
        utility_provider: 'mock', utility_model: 'mock-model',
    });
    console.log('[diag] mock provider configured');

    // ── Project (fresh test server has none) ─────────────────────────────────
    let projects = getJson('/api/projects');
    let project;
    if (!projects.length) {
        project = postJson('/api/projects', { name: 'diag-project', path: '/tmp/kanban-test-repo' });
        projects = getJson('/api/projects');
    } else {
        project = projects[0];
    }

    const browser = await chromium.launch();
    const context = await browser.newContext();
    await context.addCookies([{ name: 'PHPSESSID', value: phpsessid, domain: 'localhost', path: '/', httpOnly: true, secure: false, sameSite: 'Lax' }]);
    const page = await context.newPage();
    page.on('pageerror', e => console.log('[pageerror]', String(e).slice(0, 300)));

    const createTask = async (label) => {
        const res = postJson(`/api/projects/${project.id}/tasks`, {
            prompt: `slow-start ${label}-${Date.now()}`,
            llama_server_url: MOCK_URL, model_name: 'mock-model',
        });
        const id = res.id || res.task?.id;
        if (!id) { console.error('task creation failed:', JSON.stringify(res)); process.exit(2); }
        return id;
    };
    const openTask = async (taskId) => {
        await page.goto(`${BASE}/?project=${project.id}&task=${taskId}`);
        await page.waitForSelector('.task-detail-page', { timeout: 20000 });
        // Chat history loaded + WS subscribed (prompt message rendered).
        await page.waitForFunction(() => !!document.querySelector('.chat-message.user'), null, { timeout: 20000 });
    };

    const results = [];

    // ═══ Scenario 1: API resume on an unarmed open chat (bug repro) ═══════════
    console.log('\n=== Scenario 1: API resume (card Resume / offline-queue path) ===');
    const task1 = await createTask('resume-api');
    await openTask(task1);
    console.log('[diag] chat open, unarmed (task backlog)');
    const indicator1 = page.locator('.awaiting-llm');
    const before1 = await visible(indicator1);
    console.log(`[diag] indicator visible BEFORE resume = ${before1} (expect false)`);

    const resumeRes = postJson(`/api/projects/${project.id}/tasks/${task1}/resume`, {});
    console.log(`[diag] POST /resume → execution_state=${resumeRes.execution_state}`);
    const appear1 = await pollUntil(() => visible(indicator1), SLOW_WINDOW_MS);
    console.log(`[diag] indicator appeared after resume = ${appear1 >= 0} (${appear1}ms)`);
    const hide1 = await pollUntil(async () => !(await visible(indicator1)), 40000, 300);
    console.log(`[diag] indicator disappeared after response = ${hide1 >= 0} (${hide1}ms)`);
    results.push({ name: 'API resume shows indicator', pass: !before1 && appear1 >= 0 && hide1 >= 0 });

    // ═══ Scenario 2: ReviewBar Resume (handleStartTask arm — regression) ═════
    console.log('\n=== Scenario 2: ReviewBar Resume ===');
    const task2 = await createTask('resume-review');
    const moveRes = postJson(`/api/projects/${project.id}/tasks/${task2}/move`, { status: 'review' });
    console.log(`[diag] moved task to review (ok=${moveRes.status === 'review' || moveRes.id ? true : false})`);
    await openTask(task2);
    const resumeBtn = page.locator('.btn-commit-done', { hasText: 'Resume' });
    const btnVisible = await resumeBtn.isVisible().catch(() => false);
    console.log(`[diag] ReviewBar Resume button visible = ${btnVisible}`);
    if (btnVisible) await resumeBtn.click();

    const indicator2 = page.locator('.awaiting-llm');
    const appear2 = await pollUntil(() => visible(indicator2), SLOW_WINDOW_MS);
    console.log(`[diag] indicator appeared after ReviewBar Resume = ${appear2 >= 0} (${appear2}ms)`);
    const hide2 = await pollUntil(async () => !(await visible(indicator2)), 40000, 300);
    console.log(`[diag] indicator disappeared after response = ${hide2 >= 0} (${hide2}ms)`);
    results.push({ name: 'ReviewBar Resume shows indicator', pass: btnVisible && appear2 >= 0 && hide2 >= 0 });

    // ═══ Scenario 3: normal message send (sendWithTracking arm — regression) ═
    console.log('\n=== Scenario 3: normal chat message send ===');
    const task3 = await createTask('send-normal');
    await openTask(task3);
    // Start the task so the chat form (textarea + send button) appears.
    const startRes = postJson(`/api/projects/${project.id}/tasks/${task3}/resume`, {});
    console.log(`[diag] started task → execution_state=${startRes.execution_state}`);
    // Wait for the first (slow-start) prompt response to finish → send button ready.
    await page.waitForSelector('.btn-send', { timeout: 60000 });
    await pollUntil(async () => !(await visible(page.locator('.awaiting-llm'))), 40000, 300);

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('slow-start normal-send regression');
    await chatTextarea.press('Enter');
    const indicator3 = page.locator('.awaiting-llm');
    const appear3 = await pollUntil(() => visible(indicator3), SLOW_WINDOW_MS);
    console.log(`[diag] indicator appeared after message send = ${appear3 >= 0} (${appear3}ms)`);
    const hide3 = await pollUntil(async () => !(await visible(indicator3)), 40000, 300);
    console.log(`[diag] indicator disappeared after response = ${hide3 >= 0} (${hide3}ms)`);
    results.push({ name: 'Normal send shows indicator', pass: appear3 >= 0 && hide3 >= 0 });

    await page.screenshot({ path: '/tmp/diag-resume.png' });
    await browser.close();

    // ── Summary ───────────────────────────────────────────────────────────────
    console.log('\n=== Summary ===');
    let allPass = true;
    for (const r of results) {
        console.log(`  ${r.pass ? 'PASS' : 'FAIL'}: ${r.name}`);
        if (!r.pass) allPass = false;
    }
    console.log(allPass ? '\nALL SCENARIOS PASS' : '\nFAILURES DETECTED');
    process.exit(allPass ? 0 : 1);
}

main().catch(e => { console.error('ERROR:', e); process.exit(2); });
