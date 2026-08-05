/**
 * Global setup/teardown for e2e tests.
 * Set USE_MOCK_LLM=0 to use real LLM endpoint (default: mock).
 */

import { spawn } from 'node:child_process';
import http from 'node:http';
import fs from 'node:fs';
import path from 'node:path';
import { execSync } from 'node:child_process';

const __dirname = path.dirname(new URL(import.meta.url).pathname);
const PROJECT_ROOT = path.resolve(__dirname, '../..');
const FRONTEND_DIR = path.resolve(__dirname, '..');

const TEST_REPO = '/tmp/kanban-test-repo';
const TEST_REPO_2 = '/tmp/kanban-test-repo-2';
const TEST_DATA_DIR = '/tmp/kanban-test-data';
const TEST_DATA_DIR_ISOLATED = '/tmp/kanban-test-data-isolated';
const TEST_WORKTREES = '/tmp/kanban-test-worktrees';
const KANBAN_PORT = 3000;
const KANBAN_PORT_ISOLATED = 3001;
const MOCK_PORT = 9999;
const KANBAN_BASE = `http://localhost:${KANBAN_PORT}`;
const KANBAN_BASE_ISOLATED = `http://localhost:${KANBAN_PORT_ISOLATED}`;
const LOCK_FILE = '/tmp/kanban-test-setup.lock';
// Playwright storage state files — read at context creation time
const STORAGE_STATE_FILE = '/tmp/kanban-test-storage-state.json';
const STORAGE_STATE_FILE_ISOLATED = '/tmp/kanban-test-storage-state.json.isolated';
const KANBAN_BIN = 'go';
const KANBAN_RUN_ARGS = ['run', '.'];
const TEST_PASSWORD = 'test-password-1234';

let mockServer: ReturnType<typeof spawn> | null = null;
let kanbanServer: ReturnType<typeof spawn> | null = null;
let kanbanServerIsolated: ReturnType<typeof spawn> | null = null;

async function setup(): Promise<void> {
    ensureTestRepo();
    await killKanbanServer();

    console.log('[global-setup] Starting test kanban server...');
    kanbanServer = spawn('setsid', [KANBAN_BIN, ...KANBAN_RUN_ARGS, '--data-dir', TEST_DATA_DIR], {
        cwd: PROJECT_ROOT,
        env: { ...process.env, MIKE_WORKTREES_DIR: TEST_WORKTREES, MIKE_LOGIN_PASSWORD: TEST_PASSWORD, MIKE_TLS: '0', DANGER_ZONE: '1' },
        stdio: 'ignore',
        detached: true,
    });
    kanbanServer.unref();

    await waitForServer(KANBAN_PORT, 15000);
    console.log(`[global-setup] Test kanban server ready on port ${KANBAN_PORT}`);

    const { cookie: sessionCookie, csrfToken } = await login(KANBAN_BASE);
    fs.writeFileSync(STORAGE_STATE_FILE, JSON.stringify({
        cookies: [{
            name: 'PHPSESSID',
            value: sessionCookie.split('=')[1],
            domain: 'localhost',
            path: '/',
            httpOnly: true,
            secure: false,
            sameSite: 'Lax',
            expires: Date.now() / 1000 + 86400 * 365,
        }],
        origins: [],
    }));
    // Legacy auth file for CSRF token
    fs.writeFileSync('/tmp/kanban-test-auth.json', JSON.stringify({
        cookies: [{ name: 'PHPSESSID', value: sessionCookie.split('=')[1], domain: 'localhost', path: '/', httpOnly: true, secure: false, sameSite: 'Lax' }],
        csrfToken,
    }));
    console.log(`[global-setup] Logged in to test server`);

    const useMock = process.env.MIKE_USE_MOCK_LLM !== '0';
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
            }],
            worker_provider: 'mock',
            worker_model: 'mock-model',
            utility_provider: 'mock',
            utility_model: 'mock-model',
        }, sessionCookie, csrfToken);
        console.log(`[global-setup] Mock provider configured at ${mockUrl}`);
    }

    // Start mock LLM before creating tasks (title-gen calls LLM immediately)
    if (useMock) {
        console.log('[global-setup] Starting mock LLM server...');
        mockServer = spawn('setsid', ['node', 'tests/mock-llm.js', MOCK_PORT.toString()], {
            cwd: FRONTEND_DIR,
            stdio: 'ignore',
            detached: true,
        });
        mockServer.unref();
        await waitForServer(MOCK_PORT);
        console.log(`[global-setup] Mock LLM server ready on port ${MOCK_PORT}`);
    } else {
        console.log('[global-setup] Using real LLM endpoint (MIKE_USE_MOCK_LLM=0)');
    }

    // Create test project
    const project = await postJson(`${KANBAN_BASE}/api/projects`, {
        name: 'test-project',
        path: TEST_REPO,
    }, sessionCookie, csrfToken);
    console.log(`[global-setup] Test project created: ${project.id}`);

    // Create test tasks
    const taskPayload = useMock
        ? { prompt: 'Test task for e2e tests', llama_server_url: mockUrl, model_name: 'mock-model' }
        : { prompt: 'Test task for e2e tests' };
    const taskPayload2 = useMock
        ? { prompt: 'Second test task', llama_server_url: mockUrl, model_name: 'mock-model' }
        : { prompt: 'Second test task' };
    const task1 = await postJson(`${KANBAN_BASE}/api/projects/${project.id}/tasks`, taskPayload, sessionCookie, csrfToken);
    const task2 = await postJson(`${KANBAN_BASE}/api/projects/${project.id}/tasks`, taskPayload2, sessionCookie, csrfToken);
    console.log(`[global-setup] Test tasks created: ${task1.id}, ${task2.id}`);

    // Start isolated server
    console.log('[global-setup] Starting isolated kanban server...');
    kanbanServerIsolated = spawn('setsid', [KANBAN_BIN, ...KANBAN_RUN_ARGS, '--data-dir', TEST_DATA_DIR_ISOLATED, '--port', String(KANBAN_PORT_ISOLATED)], {
        cwd: PROJECT_ROOT,
        env: { ...process.env, MIKE_WORKTREES_DIR: TEST_WORKTREES, MIKE_LOGIN_PASSWORD: TEST_PASSWORD, MIKE_TLS: '0', DANGER_ZONE: '1' },
        stdio: 'ignore',
        detached: true,
    });
    kanbanServerIsolated.unref();
    await waitForServer(KANBAN_PORT_ISOLATED, 15000);
    console.log(`[global-setup] Isolated kanban server ready on port ${KANBAN_PORT_ISOLATED}`);

    const { cookie: sessionCookieIso, csrfToken: csrfTokenIso } = await login(KANBAN_BASE_ISOLATED);
    fs.writeFileSync(STORAGE_STATE_FILE_ISOLATED, JSON.stringify({
        cookies: [{
            name: 'PHPSESSID',
            value: sessionCookieIso.split('=')[1],
            domain: 'localhost',
            path: '/',
            httpOnly: true,
            secure: false,
            sameSite: 'Lax',
            expires: Date.now() / 1000 + 86400 * 365,
        }],
        origins: [],
    }));
    // Legacy auth file for CSRF token
    fs.writeFileSync('/tmp/kanban-test-auth.json.isolated', JSON.stringify({
        cookies: [{ name: 'PHPSESSID', value: sessionCookieIso.split('=')[1], domain: 'localhost', path: '/', httpOnly: true, secure: false, sameSite: 'Lax' }],
        csrfToken: csrfTokenIso,
    }));
    console.log(`[global-setup] Logged in to isolated server`);

    // Configure mock provider on isolated server
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
            }],
            worker_provider: 'mock',
            worker_model: 'mock-model',
            utility_provider: 'mock',
            utility_model: 'mock-model',
        }, sessionCookieIso, csrfTokenIso);
        console.log(`[global-setup] Mock provider configured on isolated server`);
    }

    // Create test project on isolated server
    const projectIso = await postJson(`${KANBAN_BASE_ISOLATED}/api/projects`, {
        name: 'test-project',
        path: TEST_REPO,
    }, sessionCookieIso, csrfTokenIso);
    console.log(`[global-setup] Test project created on isolated server: ${projectIso.id}`);

    // Create test tasks on isolated server
    const taskIso = await postJson(`${KANBAN_BASE_ISOLATED}/api/projects/${projectIso.id}/tasks`, taskPayload, sessionCookieIso, csrfTokenIso);
    const taskIso2 = await postJson(`${KANBAN_BASE_ISOLATED}/api/projects/${projectIso.id}/tasks`, taskPayload2, sessionCookieIso, csrfTokenIso);
    console.log(`[global-setup] Test tasks created on isolated server: ${taskIso.id}, ${taskIso2.id}`);

    // Create lock file
    try { fs.writeFileSync(LOCK_FILE, ''); } catch { /* ignore */ }
}

async function teardown(): Promise<void> {
    console.error('[teardown] ENTRY');
    // Stop servers.
    for (const port of [MOCK_PORT, KANBAN_PORT_ISOLATED, KANBAN_PORT]) {
        console.error(`[teardown] killing port ${port}...`);
        killPort(port);
        console.error(`[teardown] port ${port} stopped`);
    }

    // Clean up orphaned sandbox containers
    try {
        const containers = execSync('sudo podman --runtime runsc ps --filter name=kanban-sandbox -q 2>/dev/null || true').toString().trim();
        if (containers) {
            execSync('sudo podman --runtime runsc rm -f ' + containers, { stdio: 'inherit' });
            console.log('[global-teardown] Orphaned sandbox containers cleaned up');
        }
    } catch {
        console.log('[global-teardown] Warning: failed to clean up sandbox containers');
    }

    // Delete test data
    try {
        fs.rmSync(TEST_DATA_DIR, { recursive: true, force: true });
        console.log('[global-teardown] Test data directory deleted');
    } catch {
        console.log('[global-teardown] Test data directory already gone');
    }

    // Delete isolated test data
    try {
        fs.rmSync(TEST_DATA_DIR_ISOLATED, { recursive: true, force: true });
        console.log('[global-teardown] Isolated test data directory deleted');
    } catch {
        console.log('[global-teardown] Isolated test data directory already gone');
    }

    // Delete test worktrees
    try {
        fs.rmSync(TEST_WORKTREES, { recursive: true, force: true });
        console.log('[global-teardown] Test worktrees directory deleted');
    } catch {
        console.log('[global-teardown] Test worktrees already gone');
    }

    // Clean up lock and auth files
    try { fs.unlinkSync(LOCK_FILE); } catch { /* ignore */ }
    try { fs.unlinkSync(STORAGE_STATE_FILE); } catch { /* ignore */ }
    try { fs.unlinkSync(STORAGE_STATE_FILE_ISOLATED); } catch { /* ignore */ }
    try { fs.unlinkSync('/tmp/kanban-test-auth.json'); } catch { /* ignore */ }
    try { fs.unlinkSync('/tmp/kanban-test-auth.json.isolated'); } catch { /* ignore */ }
}

// --- Helpers ---

function ensureTestRepo(): void {
    // Recreate test repos for clean state
    for (const repo of [TEST_REPO, TEST_REPO_2]) {
        const label = repo === TEST_REPO ? 'test git repo' : 'second test git repo';
        // Remove existing repo
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

/** Kill processes on a port. */
function killPort(port: number): void {
    try {
        const output = execSync(`lsof -ti:${port}`, { encoding: 'utf-8', timeout: 3000 }).trim();
        if (!output) return;
        const myPid = String(process.pid);
        for (const pid of output.split('\n')) {
            const p = pid.trim();
            if (p === myPid) continue;
            try { process.kill(parseInt(p), 'SIGTERM'); } catch { /* ignore */ }
        }
    } catch { /* no processes on this port */ }
}

function cleanupStaleState(keepLockFile: boolean): void {
    if (!keepLockFile) {
        try { fs.unlinkSync(LOCK_FILE); } catch { /* ignore */ }
    }
    // Kill stale processes (no shell pipeline to avoid self-kill)
    for (const port of [MOCK_PORT, KANBAN_PORT, KANBAN_PORT_ISOLATED]) {
        killPort(port);
    }
    // Clean stale data
    try { fs.rmSync(TEST_DATA_DIR, { recursive: true, force: true }); } catch { /* ignore */ }
    try { fs.rmSync(TEST_DATA_DIR_ISOLATED, { recursive: true, force: true }); } catch { /* ignore */ }
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

    if (!killed) return;

    // Wait for port to free (up to 5s)
    for (let attempt = 0; attempt < 10; attempt++) {
        const isFree = await isPortFree(KANBAN_PORT);
        if (isFree) return;
        await new Promise(r => setTimeout(r, 500));
    }
}

/** Check if port is free. */
async function isPortFree(port: number): Promise<boolean> {
    return new Promise((resolve) => {
        const req = http.get(`http://localhost:${port}/`, () => {
            req.destroy();
            resolve(false);
        });
        req.on('error', () => {
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
                const cookie = Array.isArray(setCookie) ? setCookie[0] : setCookie;
                resolve({ cookie: cookie.split(';')[0].trim(), csrfToken: csrfToken || '' });
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
    // Only teardown if lock exists AND servers are running.
    // Stale lock + dead servers => clean up and run setup fresh.
    const lockExists = fs.existsSync(LOCK_FILE);
    const serversRunning = await isPortFree(KANBAN_PORT).then(free => !free);

    if (lockExists && serversRunning) {
        console.log('[global-setup] Teardown phase: stopping running servers');
        cleanupStaleState(true);
        await teardown();
        try { fs.unlinkSync(LOCK_FILE); } catch { /* ignore */ }
    } else {
        if (lockExists && !serversRunning) {
            console.log('[global-setup] Stale lock detected, cleaning up');
        }
        cleanupStaleState(false);
    }
    await setup();
}
