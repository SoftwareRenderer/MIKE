/**
 * Global setup/teardown for e2e tests.
 * 
 * Setup phase:
 *  1. Ensure test git repo exists
 *  2. Kill any existing kanban server on port 3000
 *  3. Start a fresh kanban server with a test-specific database
 *  4. Create a test project pointing to the test repo
 *  5. Create test tasks (so tests have cards to interact with)
 *  6. Start mock LLM server
 *  7. Set in-memory override to use mock LLM
 *
 * Teardown phase:
 *  1. Clear in-memory override
 *  2. Stop mock LLM server
 *  3. Stop test kanban server
 *  4. Delete test database and worktrees
 *  5. Remove lock file
 *
 * Set USE_MOCK_LLM=0 to use real LLM endpoint (default: mock enabled).
 */

import { spawn } from 'node:child_process';
import http from 'node:http';
import fs from 'node:fs';
import { execSync } from 'node:child_process';

const TEST_REPO = '/tmp/kanban-test-repo';
const TEST_DB = '/tmp/kanban-test.db';
const TEST_WORKTREES = '/tmp/kanban-test-worktrees';
const KANBAN_PORT = 3000;
const MOCK_PORT = 9999;
const KANBAN_BASE = `http://localhost:${KANBAN_PORT}`;
const LOCK_FILE = '/tmp/kanban-test-setup.lock';
const KANBAN_BIN = '/home/mwren/projects/kanban/kanban';

let mockServer: ReturnType<typeof spawn> | null = null;
let kanbanServer: ReturnType<typeof spawn> | null = null;

async function setup(): Promise<void> {
    // 1. Ensure test git repo exists
    ensureTestRepo();

    // 2. Clean up any stale state
    cleanupStaleState();

    // 3. Kill any existing kanban server on port 3000
    killKanbanServer();

    // 4. Start fresh kanban server with test database
    console.log('[global-setup] Starting test kanban server...');
    kanbanServer = spawn('node', ['-e', `
        const { spawn } = require('child_process');
        const child = spawn('${KANBAN_BIN}', ['-db-path', '${TEST_DB}'], {
            cwd: '/home/mwren/projects/kanban',
            env: { ...process.env, WORKTREES_DIR: '${TEST_WORKTREES}' },
            stdio: ['ignore', 'pipe', 'pipe'],
            detached: false,
        });
        child.stdout.on('data', d => process.stderr.write('[kanban] ' + d));
        child.stderr.on('data', d => process.stderr.write('[kanban] ' + d));
        process.on('SIGTERM', () => child.kill('SIGTERM'));
        process.on('SIGINT', () => child.kill('SIGINT'));
        // Keep alive
        setInterval(() => {}, 1000);
    `], {
        stdio: ['ignore', 'pipe', 'pipe'],
        detached: false,
    });

    kanbanServer.stderr.on('data', data => process.stderr.write(data.toString()));

    // 5. Wait for server to be ready
    await waitForServer(KANBAN_PORT, 15000);
    console.log(`[global-setup] Test kanban server ready on port ${KANBAN_PORT}`);

    // 6. Create test project
    const project = await postJson(`${KANBAN_BASE}/api/projects`, {
        name: 'test-project',
        path: TEST_REPO,
    });
    console.log(`[global-setup] Test project created: ${project.id}`);

    // 7. Create test tasks so tests have cards to work with
    const task1 = await postJson(`${KANBAN_BASE}/api/projects/${project.id}/tasks`, {
        prompt: 'Test task for e2e tests',
    });
    const task2 = await postJson(`${KANBAN_BASE}/api/projects/${project.id}/tasks`, {
        prompt: 'Second test task',
    });
    console.log(`[global-setup] Test tasks created: ${task1.id}, ${task2.id}`);

    // 8. Start mock LLM server (if enabled)
    const useMock = process.env.USE_MOCK_LLM !== '0';
    if (useMock) {
        console.log('[global-setup] Starting mock LLM server...');
        mockServer = spawn('node', ['tests/mock-llm.js', MOCK_PORT.toString()], {
            cwd: '/home/mwren/projects/kanban/frontend',
            stdio: ['ignore', 'pipe', 'pipe'],
        });

        mockServer.stderr.on('data', data => console.error('[mock]', data.toString().trim()));

        await waitForServer(MOCK_PORT);

        // 9. Set in-memory override (does NOT modify the database)
        await postJson(`${KANBAN_BASE}/api/test/override`, {
            llama_server_url: `http://localhost:${MOCK_PORT}/v1`,
            model_name: 'mock-model',
        });
        console.log(`[global-setup] In-memory override set to mock LLM on port ${MOCK_PORT}`);
    } else {
        console.log('[global-setup] Using real LLM endpoint (USE_MOCK_LLM=0)');
    }

    // 10. Create lock file to signal setup complete
    try { fs.writeFileSync(LOCK_FILE, ''); } catch { /* ignore */ }
}

async function teardown(): Promise<void> {
    const useMock = process.env.USE_MOCK_LLM !== '0';

    // 1. Clear in-memory override
    if (useMock) {
        try {
            await deleteRequest(`${KANBAN_BASE}/api/test/override`);
            console.log('[global-teardown] In-memory override cleared');
        } catch (e) {
            console.error('[global-teardown] Failed to clear override:', e);
        }
    }

    // 2. Stop mock LLM server
    if (mockServer) {
        mockServer.kill('SIGTERM');
        console.log('[global-teardown] Mock LLM server stopped');
    }

    // 3. Stop test kanban server
    if (kanbanServer) {
        kanbanServer.kill('SIGTERM');
        console.log('[global-teardown] Test kanban server stopped');
    }

    // 4. Delete test database
    try {
        fs.unlinkSync(TEST_DB);
        console.log('[global-teardown] Test database deleted');
    } catch {
        console.log('[global-teardown] Test database already gone');
    }

    // 5. Delete test worktrees directory
    try {
        fs.rmSync(TEST_WORKTREES, { recursive: true, force: true });
        console.log('[global-teardown] Test worktrees directory deleted');
    } catch {
        console.log('[global-teardown] Test worktrees already gone');
    }

    // 6. Remove lock file
    try { fs.unlinkSync(LOCK_FILE); } catch { /* ignore */ }
}

// --- Helpers ---

function ensureTestRepo(): void {
    if (!fs.existsSync(TEST_REPO)) {
        console.log('[global-setup] Creating test git repo...');
        fs.mkdirSync(TEST_REPO, { recursive: true });
        fs.writeFileSync(`${TEST_REPO}/README.md`, '# Test Repository\n');
        try {
            execSync('git init', { cwd: TEST_REPO });
            execSync('git add README.md', { cwd: TEST_REPO });
            execSync('git -c user.email="test@kanban.local" -c user.name="Test" commit -m "Initial commit"', { cwd: TEST_REPO });
        } catch (e) {
            console.error('[global-setup] Failed to create test repo:', e);
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
    // Clean up stale test DB/worktrees
    try { fs.unlinkSync(TEST_DB); } catch { /* ignore */ }
    try { fs.rmSync(TEST_WORKTREES, { recursive: true, force: true }); } catch { /* ignore */ }
}

function killKanbanServer(): void {
    try {
        const pids = execSync('lsof -ti:3000 2>/dev/null || true').toString().trim();
        if (pids) {
            execSync(`kill ${pids} 2>/dev/null || true`);
            console.log('[global-setup] Killed existing server on port 3000');
        }
    } catch { /* no server running */ }
    // Wait for port to be free
    let attempts = 0;
    while (attempts < 10) {
        try {
            const req = http.get(`http://localhost:${KANBAN_PORT}/`, () => {
                // Still responding, try again
            });
            req.on('error', () => {
                // Port is free
                return;
            });
            req.end();
        } catch { /* port is free */ }
        attempts++;
        if (attempts < 10) {
            // Small delay
            const start = Date.now();
            while (Date.now() - start < 500) { /* busy wait */ }
        }
    }
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

function postJson(url: string, body: any): Promise<any> {
    return new Promise((resolve, reject) => {
        const data = JSON.stringify(body);
        const req = http.request(url, {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json',
                'Content-Length': Buffer.byteLength(data),
            },
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
