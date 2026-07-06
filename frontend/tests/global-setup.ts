/**
 * Global setup/teardown for e2e tests.
 * 
 * Setup phase:
 *  1. Ensure test git repo exists
 *  2. Kill any existing kanban server on port 3000
 *  3. Start a fresh kanban server with a test-specific database
 *  4. Configure mock provider via API
 *  5. Create a test project pointing to the test repo
 *  6. Create test tasks with mock provider URL
 *  7. Start mock LLM server
 *
 * Teardown phase:
 *  1. Stop mock LLM server
 *  2. Stop test kanban server
 *  3. Delete test database and worktrees
 *  4. Remove lock file
 *
 * Set USE_MOCK_LLM=0 to use real LLM endpoint (default: mock enabled).
 */

import { spawn } from 'node:child_process';
import http from 'node:http';
import fs from 'node:fs';
import { execSync } from 'node:child_process';

const TEST_REPO = '/tmp/kanban-test-repo';
const TEST_REPO_2 = '/tmp/kanban-test-repo-2';
const TEST_DB = '/tmp/kanban-test.db';
const TEST_DB_ISOLATED = '/tmp/kanban-test-isolated.db';
const TEST_WORKTREES = '/tmp/kanban-test-worktrees';
const KANBAN_PORT = 3000;
const KANBAN_PORT_ISOLATED = 3001;
const MOCK_PORT = 9999;
const KANBAN_BASE = `http://localhost:${KANBAN_PORT}`;
const KANBAN_BASE_ISOLATED = `http://localhost:${KANBAN_PORT_ISOLATED}`;
const LOCK_FILE = '/tmp/kanban-test-setup.lock';
const AUTH_FILE = '/tmp/kanban-test-auth.json';
const KANBAN_BIN = '/home/mwren/projects/kanban/kanban';
const TEST_PASSWORD = 'test-password-1234';

let mockServer: ReturnType<typeof spawn> | null = null;
let kanbanServer: ReturnType<typeof spawn> | null = null;
let kanbanServerIsolated: ReturnType<typeof spawn> | null = null;

async function setup(): Promise<void> {
    // 1. Ensure test git repo exists
    ensureTestRepo();

    // 2. Kill any existing kanban server on port 3000
    await killKanbanServer();

    // 4. Start fresh kanban server with test database
    console.log('[global-setup] Starting test kanban server...');
    // Use setsid to start server in a new session (truly independent of this process)
    kanbanServer = spawn('setsid', [KANBAN_BIN, '-db-path', TEST_DB], {
        cwd: '/home/mwren/projects/kanban',
        env: { ...process.env, WORKTREES_DIR: TEST_WORKTREES, KANBAN_PASSWORD: TEST_PASSWORD, KANBAN_TLS: '0' },
        stdio: 'ignore',
        detached: true,
    });
    kanbanServer.unref();

    // 5. Wait for server to be ready
    await waitForServer(KANBAN_PORT, 15000);
    console.log(`[global-setup] Test kanban server ready on port ${KANBAN_PORT}`);

    // 5b. Login to get session cookie and CSRF token
    const { cookie: sessionCookie, csrfToken } = await login(KANBAN_BASE);
    // Write auth info for Playwright to pick up
    fs.writeFileSync(AUTH_FILE, JSON.stringify({
        cookies: [{ name: 'PHPSESSID', value: sessionCookie.split('=')[1], domain: 'localhost', path: '/', httpOnly: true, secure: false, sameSite: 'Lax' }],
        csrfToken,
    }));
    console.log(`[global-setup] Logged in to test server`);

    // 6. Configure mock provider via API (if using mock LLM)
    const useMock = process.env.USE_MOCK_LLM !== '0';
    const mockUrl = `http://localhost:${MOCK_PORT}/v1`;
    if (useMock) {
        const config = await getJson(`${KANBAN_BASE}/api/config`, sessionCookie);
        await putJson(`${KANBAN_BASE}/api/config`, {
            ...config,
            providers: [{
                name: 'mock',
                display_name: 'Mock LLM',
                url: mockUrl,
                models: [{ name: 'mock-model', reasoning_effort: 'off' }],
                concurrency: 16,
                default_model: 'mock-model',
            }],
            default_provider: 'mock',
        }, sessionCookie, csrfToken);
        console.log(`[global-setup] Mock provider configured at ${mockUrl}`);
    }

    // 7. Start mock LLM server BEFORE creating tasks (title-gen goroutine calls LLM immediately)
    if (useMock) {
        console.log('[global-setup] Starting mock LLM server...');
        mockServer = spawn('setsid', ['node', 'tests/mock-llm.js', MOCK_PORT.toString()], {
            cwd: '/home/mwren/projects/kanban/frontend',
            stdio: 'ignore',
            detached: true,
        });
        mockServer.unref();
        await waitForServer(MOCK_PORT);
        console.log(`[global-setup] Mock LLM server ready on port ${MOCK_PORT}`);
    } else {
        console.log('[global-setup] Using real LLM endpoint (USE_MOCK_LLM=0)');
    }

    // 8. Create test project (after mock LLM is ready, so title-gen works)
    const project = await postJson(`${KANBAN_BASE}/api/projects`, {
        name: 'test-project',
        path: TEST_REPO,
    }, sessionCookie, csrfToken);
    console.log(`[global-setup] Test project created: ${project.id}`);

    // 9. Create test tasks with provider URL
    const taskPayload = useMock
        ? { prompt: 'Test task for e2e tests', llama_server_url: mockUrl, model_name: 'mock-model' }
        : { prompt: 'Test task for e2e tests' };
    const taskPayload2 = useMock
        ? { prompt: 'Second test task', llama_server_url: mockUrl, model_name: 'mock-model' }
        : { prompt: 'Second test task' };
    const task1 = await postJson(`${KANBAN_BASE}/api/projects/${project.id}/tasks`, taskPayload, sessionCookie, csrfToken);
    const task2 = await postJson(`${KANBAN_BASE}/api/projects/${project.id}/tasks`, taskPayload2, sessionCookie, csrfToken);
    console.log(`[global-setup] Test tasks created: ${task1.id}, ${task2.id}`);

    // 10. Start isolated kanban server (for config-modifying test suites)
    console.log('[global-setup] Starting isolated kanban server...');
    kanbanServerIsolated = spawn('setsid', [KANBAN_BIN, '-db-path', TEST_DB_ISOLATED], {
        cwd: '/home/mwren/projects/kanban',
        env: { ...process.env, WORKTREES_DIR: TEST_WORKTREES, PORT: String(KANBAN_PORT_ISOLATED), KANBAN_PASSWORD: TEST_PASSWORD, KANBAN_TLS: '0' },
        stdio: 'ignore',
        detached: true,
    });
    kanbanServerIsolated.unref();
    await waitForServer(KANBAN_PORT_ISOLATED, 15000);
    console.log(`[global-setup] Isolated kanban server ready on port ${KANBAN_PORT_ISOLATED}`);

    // 10b. Login to isolated server
    const { cookie: sessionCookieIso, csrfToken: csrfTokenIso } = await login(KANBAN_BASE_ISOLATED);
    // Write isolated auth info
    fs.writeFileSync(AUTH_FILE + '.isolated', JSON.stringify({
        cookies: [{ name: 'PHPSESSID', value: sessionCookieIso.split('=')[1], domain: 'localhost', path: '/', httpOnly: true, secure: false, sameSite: 'Lax' }],
        csrfToken: csrfTokenIso,
    }));
    console.log(`[global-setup] Logged in to isolated server`);

    // 11. Configure mock provider on isolated server
    if (useMock) {
        const configIso = await getJson(`${KANBAN_BASE_ISOLATED}/api/config`, sessionCookieIso);
        await putJson(`${KANBAN_BASE_ISOLATED}/api/config`, {
            ...configIso,
            providers: [{
                name: 'mock',
                display_name: 'Mock LLM',
                url: mockUrl,
                models: [{ name: 'mock-model', reasoning_effort: 'off' }],
                concurrency: 16,
                default_model: 'mock-model',
            }],
            default_provider: 'mock',
        }, sessionCookieIso, csrfTokenIso);
        console.log(`[global-setup] Mock provider configured on isolated server`);
    }

    // 12. Create test project on isolated server
    const projectIso = await postJson(`${KANBAN_BASE_ISOLATED}/api/projects`, {
        name: 'test-project',
        path: TEST_REPO,
    }, sessionCookieIso, csrfTokenIso);
    console.log(`[global-setup] Test project created on isolated server: ${projectIso.id}`);

    // 13. Create test tasks on isolated server
    const taskIso = await postJson(`${KANBAN_BASE_ISOLATED}/api/projects/${projectIso.id}/tasks`, taskPayload, sessionCookieIso, csrfTokenIso);
    const taskIso2 = await postJson(`${KANBAN_BASE_ISOLATED}/api/projects/${projectIso.id}/tasks`, taskPayload2, sessionCookieIso, csrfTokenIso);
    console.log(`[global-setup] Test tasks created on isolated server: ${taskIso.id}, ${taskIso2.id}`);

    // 14. Create lock file to signal setup complete
    try { fs.writeFileSync(LOCK_FILE, ''); } catch { /* ignore */ }
}

async function teardown(): Promise<void> {
    // 1. Stop mock LLM server
    try { execSync('lsof -ti:' + MOCK_PORT + ' | xargs kill 2>/dev/null || true'); } catch { /* ignore */ }
    console.log('[global-teardown] Mock LLM server stopped');

    // 2. Stop isolated kanban server
    try { execSync('lsof -ti:' + KANBAN_PORT_ISOLATED + ' | xargs kill 2>/dev/null || true'); } catch { /* ignore */ }
    console.log('[global-teardown] Isolated kanban server stopped');

    // 3. Stop test kanban server
    try { execSync('lsof -ti:' + KANBAN_PORT + ' | xargs kill 2>/dev/null || true'); } catch { /* ignore */ }
    console.log('[global-teardown] Test kanban server stopped');

    // 4. Clean up orphaned sandbox containers
    try {
        const containers = execSync('sudo podman --runtime runsc ps --filter name=kanban-sandbox -q 2>/dev/null || true').toString().trim();
        if (containers) {
            execSync('sudo podman --runtime runsc rm -f ' + containers, { stdio: 'inherit' });
            console.log('[global-teardown] Orphaned sandbox containers cleaned up');
        }
    } catch {
        console.log('[global-teardown] Warning: failed to clean up sandbox containers');
    }

    // 5. Delete test database
    try {
        fs.unlinkSync(TEST_DB);
        console.log('[global-teardown] Test database deleted');
    } catch {
        console.log('[global-teardown] Test database already gone');
    }

    // 6. Delete isolated test database
    try {
        fs.unlinkSync(TEST_DB_ISOLATED);
        console.log('[global-teardown] Isolated test database deleted');
    } catch {
        console.log('[global-teardown] Isolated test database already gone');
    }

    // 7. Delete test worktrees directory
    try {
        fs.rmSync(TEST_WORKTREES, { recursive: true, force: true });
        console.log('[global-teardown] Test worktrees directory deleted');
    } catch {
        console.log('[global-teardown] Test worktrees already gone');
    }

    // 8. Remove lock file
    try { fs.unlinkSync(LOCK_FILE); } catch { /* ignore */ }
    // 9. Remove auth files
    try { fs.unlinkSync(AUTH_FILE); } catch { /* ignore */ }
    try { fs.unlinkSync(AUTH_FILE + '.isolated'); } catch { /* ignore */ }
}

// --- Helpers ---

function ensureTestRepo(): void {
    // Always recreate test repos to ensure clean state (no stale branches/files)
    for (const repo of [TEST_REPO, TEST_REPO_2]) {
        const label = repo === TEST_REPO ? 'test git repo' : 'second test git repo';
        // Remove existing repo and worktrees
        try { fs.rmSync(repo, { recursive: true, force: true }); } catch { /* ignore */ }
        console.log(`[global-setup] Creating ${label}...`);
        fs.mkdirSync(repo, { recursive: true });
        const readme = repo === TEST_REPO ? '# Test Repository\n' : '# Second Test Repository\n';
        fs.writeFileSync(`${repo}/README.md`, readme);
        try {
            execSync('git init', { cwd: repo });
            execSync('git add README.md', { cwd: repo });
            execSync('git -c user.email="test@kanban.local" -c user.name="Test" commit -m "Initial commit"', { cwd: repo });
        } catch (e) {
            console.error(`[global-setup] Failed to create ${label}:`, e);
            throw e;
        }
    }
}

function cleanupStaleState(): void {
    // Remove stale lock file from interrupted runs
    try { fs.unlinkSync(LOCK_FILE); } catch { /* ignore */ }
    // Kill stale mock LLM processes
    try { execSync('pkill -f "mock-llm" 2>/dev/null || true'); } catch { /* ignore */ }
    // Kill stale kanban on port 3000 (will be restarted)
    try { execSync('lsof -ti:3000 | xargs kill 2>/dev/null || true'); } catch { /* ignore */ }
    // Kill stale kanban on port 3001 (isolated server)
    try { execSync('lsof -ti:3001 | xargs kill 2>/dev/null || true'); } catch { /* ignore */ }
    // Clean up stale test DB/worktrees
    try { fs.unlinkSync(TEST_DB); } catch { /* ignore */ }
    try { fs.unlinkSync(TEST_DB_ISOLATED); } catch { /* ignore */ }
    try { fs.rmSync(TEST_WORKTREES, { recursive: true, force: true }); } catch { /* ignore */ }
}

async function killKanbanServer(): Promise<void> {
    let killed = false;
    try {
        const pids = execSync('lsof -ti:3000 2>/dev/null || true').toString().trim();
        if (pids) {
            execSync(`kill ${pids} 2>/dev/null || true`);
            console.log('[global-setup] Killed existing server on port 3000');
            killed = true;
        }
    } catch { /* no server running */ }

    if (!killed) return; // No server was running, nothing to wait for

    // Wait for port to be free (up to 5s)
    for (let attempt = 0; attempt < 10; attempt++) {
        const isFree = await isPortFree(KANBAN_PORT);
        if (isFree) return;
        await new Promise(r => setTimeout(r, 500));
    }
}

/** Return true if the port is not accepting connections. */
async function isPortFree(port: number): Promise<boolean> {
    return new Promise((resolve) => {
        const req = http.get(`http://localhost:${port}/`, () => {
            // Got a response — port is still in use
            req.destroy();
            resolve(false);
        });
        req.on('error', () => {
            // Connection refused — port is free
            resolve(true);
        });
        req.setTimeout(500, () => {
            req.destroy();
            resolve(false);
        });
        req.end();
    });
}

function waitForServer(port: number, timeout = 5000): Promise<void> {
    return new Promise((resolve, reject) => {
        const start = Date.now();
        const tryConnect = () => {
            const req = http.get(`http://localhost:${port}/`, () => {
                resolve();
            });
            req.on('error', () => {
                if (Date.now() - start < timeout) {
                    setTimeout(tryConnect, 200);
                } else {
                    reject(new Error(`Server on port ${port} did not start within ${timeout}ms`));
                }
            });
            req.end();
        };
        tryConnect();
    });
}

function postJson(url: string, body: any, cookie?: string, csrfToken?: string): Promise<any> {
    return new Promise((resolve, reject) => {
        const data = JSON.stringify(body);
        const headers: Record<string, string> = {
            'Content-Type': 'application/json',
            'Content-Length': Buffer.byteLength(data).toString(),
        };
        if (cookie) headers['Cookie'] = cookie;
        if (csrfToken) headers['X-CSRF-Token'] = csrfToken;
        const req = http.request(url, {
            method: 'POST',
            headers,
        }, (res) => {
            let responseData = '';
            res.on('data', chunk => responseData += chunk);
            res.on('end', () => {
                try { resolve(JSON.parse(responseData)); } catch { resolve(null); }
            });
        });
        req.on('error', reject);
        req.write(data);
        req.end();
    });
}

function putJson(url: string, body: any, cookie?: string, csrfToken?: string): Promise<any> {
    return new Promise((resolve, reject) => {
        const data = JSON.stringify(body);
        const headers: Record<string, string> = {
            'Content-Type': 'application/json',
            'Content-Length': Buffer.byteLength(data).toString(),
        };
        if (cookie) headers['Cookie'] = cookie;
        if (csrfToken) headers['X-CSRF-Token'] = csrfToken;
        const req = http.request(url, {
            method: 'PUT',
            headers,
        }, (res) => {
            let responseData = '';
            res.on('data', chunk => responseData += chunk);
            res.on('end', () => {
                try { resolve(JSON.parse(responseData)); } catch { resolve(null); }
            });
        });
        req.on('error', reject);
        req.write(data);
        req.end();
    });
}

function getJson(url: string, cookie?: string): Promise<any> {
    return new Promise((resolve, reject) => {
        const headers: Record<string, string> = {};
        if (cookie) headers['Cookie'] = cookie;
        const req = http.get(url, { headers }, (res) => {
            let responseData = '';
            res.on('data', chunk => responseData += chunk);
            res.on('end', () => {
                try { resolve(JSON.parse(responseData)); } catch { resolve(null); }
            });
        });
        req.on('error', reject);
        req.end();
    });
}

/** Login to the server and return the session cookie string and CSRF token. */
async function login(base: string): Promise<{ cookie: string; csrfToken: string }> {
    return new Promise((resolve, reject) => {
        const data = JSON.stringify({ password: TEST_PASSWORD });
        const req = http.request(`${base}/api/auth/login`, {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json',
                'Content-Length': Buffer.byteLength(data).toString(),
            },
        }, (res) => {
            const setCookie = res.headers['set-cookie'];
            const csrfToken = res.headers['x-csrf-token'];
            let responseData = '';
            res.on('data', chunk => responseData += chunk);
            res.on('end', () => {
                if (!setCookie || setCookie.length === 0) {
                    reject(new Error(`Login failed: ${responseData}`));
                    return;
                }
                // Extract just the PHPSESSID=token part
                const cookie = Array.isArray(setCookie) ? setCookie[0] : setCookie;
                const parts = cookie.split(';')[0].trim();
                resolve({ cookie: parts, csrfToken: csrfToken || '' });
            });
        });
        req.on('error', reject);
        req.write(data);
        req.end();
    });
}

function deleteRequest(url: string): Promise<any> {
    return new Promise((resolve, reject) => {
        const req = http.request(url, {
            method: 'DELETE',
        }, (res) => {
            let responseData = '';
            res.on('data', chunk => responseData += chunk);
            res.on('end', () => {
                try { resolve(JSON.parse(responseData)); } catch { resolve(null); }
            });
        });
        req.on('error', reject);
        req.end();
    });
}

export default async function main(): Promise<void> {
    // Always clean stale state first (from interrupted runs)
    // This must run BEFORE the lock file check, otherwise a stale lock
    // file causes teardown to run, which kills servers but doesn't restart them.
    cleanupStaleState();

    // If lock file exists, we're in teardown phase (setup already ran)
    // Otherwise, we're in setup phase
    const isTeardown = fs.existsSync(LOCK_FILE);

    if (isTeardown) {
        await teardown();
        try { fs.unlinkSync(LOCK_FILE); } catch { /* ignore */ }
    } else {
        await setup();
    }
}
