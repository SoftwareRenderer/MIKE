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
// Per-worker project repos created by helpers.ts isolatedProject() — scratch, like the worktrees.
const TEST_OWN_REPOS = '/tmp/kanban-test-repos';
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
// Point the test servers at a local registry fixture so e2e registry tests are
// deterministic and never depend on the hosted online registry.
const TEST_REGISTRY_URL = 'file://' + path.join(__dirname, 'registry-fixture.json');
// Local (unpublished) extension fixtures seeded into the repo extensions dir
// for the Browse e2e specs. In a fresh checkout `extensions/` is empty, so
// specs that need local extensions would fail; seeding from committed fixtures
// keeps the suite hermetic. Only MISSING entries are created — pre-existing
// dev installs are never touched — and teardown removes exactly what setup
// created (tracked in SEEDED_EXTENSIONS_FILE so an interrupted run's leftovers
// are cleaned by the next run's stale-state cleanup).
const LOCAL_EXT_FIXTURES_DIR = path.join(__dirname, 'fixtures', 'local-extensions');
const SEEDED_EXTENSIONS_FILE = '/tmp/kanban-test-seeded-extensions.json';

let mockServer: ReturnType<typeof spawn> | null = null;
let kanbanServer: ReturnType<typeof spawn> | null = null;
let kanbanServerIsolated: ReturnType<typeof spawn> | null = null;

async function setup(): Promise<void> {
    ensureTestRepo();
    seedLocalExtensions();
    await killKanbanServer();

    console.log('[global-setup] Starting test kanban server...');
    kanbanServer = spawn('setsid', [KANBAN_BIN, ...KANBAN_RUN_ARGS, '--data-dir', TEST_DATA_DIR], {
        cwd: PROJECT_ROOT,
        env: { ...process.env, MIKE_WORKTREES_DIR: TEST_WORKTREES, MIKE_LOGIN_PASSWORD: TEST_PASSWORD, MIKE_REGISTRY_URL: TEST_REGISTRY_URL, MIKE_TLS: '0', DANGER_ZONE: '1' },
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
        env: { ...process.env, MIKE_WORKTREES_DIR: TEST_WORKTREES, MIKE_LOGIN_PASSWORD: TEST_PASSWORD, MIKE_REGISTRY_URL: TEST_REGISTRY_URL, MIKE_TLS: '0', DANGER_ZONE: '1' },
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

export async function teardown(): Promise<void> {
    console.error('[teardown] ENTRY');
    // Stop servers — by identity first (the port does not reach the whole process tree), then by
    // port for anything else (the mock LLM).
    killTestServers();
    for (const port of [MOCK_PORT, KANBAN_PORT_ISOLATED, KANBAN_PORT]) {
        console.error(`[teardown] killing port ${port}...`);
        killPort(port);
        console.error(`[teardown] port ${port} stopped`);
    }

    // Remove local extension fixtures this (or an interrupted previous) setup created.
    removeSeededLocalExtensions();

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

    // Delete test data — only once the server has let go of it (see waitForFilesRelease)
    waitForFilesRelease(TEST_DATA_DIR);
    try {
        fs.rmSync(TEST_DATA_DIR, { recursive: true, force: true });
        console.log('[global-teardown] Test data directory deleted');
    } catch {
        console.log('[global-teardown] Test data directory already gone');
    }

    // Delete isolated test data
    waitForFilesRelease(TEST_DATA_DIR_ISOLATED);
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

    // Delete the per-worker project repos (helpers.ts isolatedProject)
    try { fs.rmSync(TEST_OWN_REPOS, { recursive: true, force: true }); } catch { /* ignore */ }

    // Clean up lock and auth files
    try { fs.unlinkSync(LOCK_FILE); } catch { /* ignore */ }
    try { fs.unlinkSync(STORAGE_STATE_FILE); } catch { /* ignore */ }
    try { fs.unlinkSync(STORAGE_STATE_FILE_ISOLATED); } catch { /* ignore */ }
    try { fs.unlinkSync('/tmp/kanban-test-auth.json'); } catch { /* ignore */ }
    try { fs.unlinkSync('/tmp/kanban-test-auth.json.isolated'); } catch { /* ignore */ }
}

// --- Local extension seeding ---

function readSeededExtensions(): string[] {
    try {
        return JSON.parse(fs.readFileSync(SEEDED_EXTENSIONS_FILE, 'utf-8')) as string[];
    } catch {
        return [];
    }
}

function writeSeededExtensions(list: string[]): void {
    try {
        fs.writeFileSync(SEEDED_EXTENSIONS_FILE, JSON.stringify(list));
    } catch { /* ignore */ }
}

/** Copy committed local-extension fixtures into the repo extensions dir, but
 *  only where the extension is not already present (dev installs win). */
function seedLocalExtensions(): void {
    let authors: string[];
    try {
        authors = fs.readdirSync(LOCAL_EXT_FIXTURES_DIR);
    } catch {
        return; // no fixtures dir — nothing to seed
    }
    const created = readSeededExtensions();
    for (const author of authors) {
        const fixtureAuthor = path.join(LOCAL_EXT_FIXTURES_DIR, author);
        if (!fs.statSync(fixtureAuthor).isDirectory()) continue;
        for (const name of fs.readdirSync(fixtureAuthor)) {
            const src = path.join(fixtureAuthor, name);
            if (!fs.statSync(src).isDirectory()) continue;
            const dest = path.join(PROJECT_ROOT, 'extensions', author, name);
            if (fs.existsSync(dest)) continue; // pre-existing dev install wins
            fs.cpSync(src, dest, { recursive: true });
            created.push(dest);
            console.log(`[global-setup] Seeded local extension fixture: ${author}/${name}`);
        }
    }
    writeSeededExtensions(created);
}

/** Remove exactly the extension dirs a (possibly previous) setup created. */
function removeSeededLocalExtensions(): void {
    for (const dir of readSeededExtensions()) {
        try {
            fs.rmSync(dir, { recursive: true, force: true });
            // Drop the now-empty author dir if setup created it too.
            fs.rmdirSync(path.dirname(dir));
        } catch { /* not empty or already gone — leave it */ }
    }
    try {
        fs.unlinkSync(SEEDED_EXTENSIONS_FILE);
    } catch { /* ignore */ }
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
            // Set a local git identity so backend commits (e.g. commit-and-done,
            // which don't pass -c flags) succeed even when no global git config exists.
            execSync('git config user.email "test@kanban.local"', { cwd: repo });
            execSync('git config user.name "Test"', { cwd: repo });
            execSync('git add README.md', { cwd: repo });
            execSync('git commit -m "Initial commit"', { cwd: repo });
        } catch (e) {
            console.error(`[global-setup] Failed to create ${label}:`, e);
            throw e;
        }
    }
}

/**
 * Stop the test servers by identity (--data-dir) and wait for them to be gone. The port is not
 * enough: the servers are grandchildren (`setsid go run . …`), so the port owner is not the process
 * that should stop, and `lsof -ti:PORT` also lists the port's clients. A server that outlives its
 * run keeps the ports and its SQLite files, so the next run boots on the previous run's database.
 * The pattern is anchored because the two data dirs share a prefix.
 */
function killTestServers(): void {
    // Isolated first: its pattern is the only one that cannot also match the main server.
    for (const dir of [TEST_DATA_DIR_ISOLATED, TEST_DATA_DIR]) {
        const pattern = `--data-dir ${dir}([[:space:]]|$)`;
        let pids = '';
        try {
            pids = execSync(`pgrep -f "${pattern}" 2>/dev/null || true`, { encoding: 'utf-8', timeout: 5000 }).trim();
        } catch { /* none */ }
        const mine = String(process.pid);
        let victims = pids ? pids.split('\n').map((x) => x.trim()).filter((x) => x && x !== mine) : [];
        if (victims.length === 0) continue;
        for (const pid of victims) { try { process.kill(parseInt(pid), 'SIGTERM'); } catch { /* ignore */ } }
        const deadline = Date.now() + 8000;
        while (Date.now() < deadline) {
            const still = survivors(pattern, mine);
            if (still.length === 0) return;
            victims = still;
            execSync('sleep 0.25');
        }
        for (const pid of victims) {
            console.error(`[global-setup] test server pid ${pid} (${dir}) ignored SIGTERM — SIGKILL`);
            try { process.kill(parseInt(pid), 'SIGKILL'); } catch { /* ignore */ }
        }
        const killed = Date.now() + 5000;
        while (Date.now() < killed && survivors(pattern, mine).length > 0) execSync('sleep 0.2');
    }
}

function survivors(pattern: string, selfPid: string): string[] {
    try {
        const out = execSync(`pgrep -f "${pattern}" 2>/dev/null || true`, { encoding: 'utf-8', timeout: 5000 }).trim();
        return out ? out.split('\n').map((x) => x.trim()).filter((x) => x && x !== selfPid) : [];
    } catch {
        return [];
    }
}

/** PIDs listening on `port`, excluding this process (killing the runner would end the suite). */
function portPids(port: number): string[] {
    try {
        const output = execSync(`lsof -ti:${port}`, { encoding: 'utf-8', timeout: 3000 }).trim();
        return output ? output.split('\n').map((p) => p.trim()).filter((p) => p && p !== String(process.pid)) : [];
    } catch {
        return [];
    }
}

/**
 * Stop whoever owns `port` and do not return until it is free. A wedged server ignores SIGTERM and
 * keeps the listener; a polite one stays bound for a moment while it shuts down.
 */
function killPort(port: number): void {
    let pids = portPids(port);
    if (pids.length === 0) return;
    for (const pid of pids) {
        try { process.kill(parseInt(pid), 'SIGTERM'); } catch { /* ignore */ }
    }
    const deadline = Date.now() + 8000;
    while (Date.now() < deadline) {
        pids = portPids(port);
        if (pids.length === 0) return;
        execSync('sleep 0.25');
    }
    for (const pid of pids) {
        console.error(`[global-setup] port ${port} pid ${pid} ignored SIGTERM — SIGKILL`);
        try { process.kill(parseInt(pid), 'SIGKILL'); } catch { /* ignore */ }
    }
    const killed = Date.now() + 3000;
    while (Date.now() < killed && portPids(port).length > 0) execSync('sleep 0.2');
}

/**
 * Wait until no process holds anything open inside `dir`. Deleting the data dir while the killed
 * server is still checkpointing SQLite leaves it re-created afterwards, and the next run then
 * recovers the PREVIOUS run's database from the leftover WAL.
 */
// Budget: server.ShutdownTimeout (the worst-case graceful exit) plus slack to notice.
function waitForFilesRelease(dir: string, timeoutMs = 20000): void {
    if (!fs.existsSync(dir)) return;
    const holders = (): string => {
        try {
            return execSync(`lsof +D ${dir} -t 2>/dev/null || true`, { encoding: 'utf-8', timeout: 5000 }).trim();
        } catch {
            return '';
        }
    };
    const deadline = Date.now() + timeoutMs;
    for (;;) {
        const open = holders();
        if (!open) return;
        if (Date.now() > deadline) {
            console.error(`[global-setup] Warning: ${dir} still held by ${open.split('\n').join(',')} — deleting anyway`);
            return;
        }
        execSync('sleep 0.25');
    }
}

function cleanupStaleState(keepLockFile: boolean): void {
    // Leftover seeded fixtures from an interrupted run.
    removeSeededLocalExtensions();
    if (!keepLockFile) {
        try { fs.unlinkSync(LOCK_FILE); } catch { /* ignore */ }
    }
    // Kill stale processes (no shell pipeline to avoid self-kill)
    killTestServers();
    for (const port of [MOCK_PORT, KANBAN_PORT, KANBAN_PORT_ISOLATED]) {
        killPort(port);
    }
    // Clean stale data — after the servers above have actually released their files
    waitForFilesRelease(TEST_DATA_DIR);
    waitForFilesRelease(TEST_DATA_DIR_ISOLATED);
    try { fs.rmSync(TEST_DATA_DIR, { recursive: true, force: true }); } catch { /* ignore */ }
    try { fs.rmSync(TEST_DATA_DIR_ISOLATED, { recursive: true, force: true }); } catch { /* ignore */ }
    try { fs.rmSync(TEST_WORKTREES, { recursive: true, force: true }); } catch { /* ignore */ }
    try { fs.rmSync(TEST_OWN_REPOS, { recursive: true, force: true }); } catch { /* ignore */ }
}

async function killKanbanServer(): Promise<void> {
    let killed = false;
    try {
        const pids = execSync('lsof -ti:3000 2>/dev/null || true').toString().trim();
        // Skip this process — killing it would SIGTERM the Playwright runner.
        const toKill = pids ? pids.split('\n').map(p => p.trim()).filter(p => p && p !== String(process.pid)) : [];
        if (toKill.length > 0) {
            execSync(`kill ${toKill.join(' ')} 2>/dev/null || true`);
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
                req.destroy(); // release the port socket
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
