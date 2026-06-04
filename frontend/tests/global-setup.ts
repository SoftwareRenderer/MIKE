/**
 * Global setup: starts mock LLM server and sets in-memory override on kanban.
 * 
 * Set USE_MOCK_LLM=0 to use real LLM endpoint (default: mock enabled).
 */

import { spawn } from 'node:child_process';
import http from 'node:http';

const MOCK_PORT = 9999;
const KANBAN_BASE = 'http://localhost:3000';

let mockServer: ReturnType<typeof spawn> | null = null;

async function setup(): Promise<void> {
    const useMock = process.env.USE_MOCK_LLM !== '0';
    if (!useMock) {
        console.log('[global-setup] Using real LLM endpoint (USE_MOCK_LLM=0)');
        return;
    }

    console.log('[global-setup] Starting mock LLM server...');
    mockServer = spawn('node', ['tests/mock-llm.js', MOCK_PORT.toString()], {
        cwd: '/home/mwren/projects/kanban/frontend',
        stdio: ['ignore', 'pipe', 'pipe'],
    });

    mockServer.stderr.on('data', () => {});

    await waitForServer(MOCK_PORT);

    // Set in-memory override (does NOT modify the database)
    await postJson(`${KANBAN_BASE}/api/test/override`, {
        llama_server_url: `http://localhost:${MOCK_PORT}/v1`,
        model_name: 'mock-model',
    });
    console.log(`[global-setup] In-memory override set to mock LLM on port ${MOCK_PORT}`);
}

async function teardown(): Promise<void> {
    const useMock = process.env.USE_MOCK_LLM !== '0';
    if (!useMock) return;

    // Clear in-memory override (database is never modified)
    try {
        await deleteRequest(`${KANBAN_BASE}/api/test/override`);
        console.log('[global-teardown] In-memory override cleared');
    } catch (e) {
        console.error('[global-teardown] Failed to clear override:', e);
    }

    // Stop mock server
    if (mockServer) {
        mockServer.kill('SIGTERM');
        console.log('[global-teardown] Mock LLM server stopped');
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
                    setTimeout(tryConnect, 100);
                } else {
                    reject(new Error(`Server on port ${port} did not start`));
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

// Playwright requires a single default export that detects setup vs teardown
// by checking if process.env.PLAYWRIGHT_PROJECT_NAME is set.
// Actually, Playwright calls globalSetup and globalTeardown as separate files.
// We need two separate files OR use the same file with detection.

// Detection approach: check if mock server is already running.
// If NOT running -> setup. If running -> teardown.

export default async function main(): Promise<void> {
    const running = await isMockRunning();
    
    if (!running) {
        await setup();
    } else {
        await teardown();
    }
}

function isMockRunning(): Promise<boolean> {
    return new Promise((resolve) => {
        const req = http.get(`http://localhost:${MOCK_PORT}/`, () => {
            resolve(true);
        });
        req.on('error', () => resolve(false));
        req.end();
    });
}
