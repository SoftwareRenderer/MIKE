/**
 * Global setup: starts mock LLM server and updates kanban config.
 * 
 * Set USE_MOCK_LLM=0 to use real LLM endpoint (default: mock enabled).
 */

import { spawn } from 'node:child_process';
import http from 'node:http';

const MOCK_PORT = 9999;
const KANBAN_BASE = 'http://localhost:3000';

let mockServer: ReturnType<typeof spawn> | null = null;
let originalConfig: any = null;

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

    // Save and update config
    originalConfig = await fetchJson(`${KANBAN_BASE}/api/config`);
    if (originalConfig) {
        await putJson(`${KANBAN_BASE}/api/config`, {
            llama_server_url: `http://localhost:${MOCK_PORT}/v1`,
            model_name: 'mock-model',
        });
        console.log(`[global-setup] Config updated to use mock LLM on port ${MOCK_PORT}`);
    }
}

async function teardown(): Promise<void> {
    const useMock = process.env.USE_MOCK_LLM !== '0';
    if (!useMock) return;

    // Restore original config
    if (originalConfig) {
        try {
            await putJson(`${KANBAN_BASE}/api/config`, {
                llama_server_url: originalConfig.llama_server_url,
                model_name: originalConfig.model_name,
            });
            console.log('[global-teardown] Config restored to original');
        } catch (e) {
            console.error('[global-teardown] Failed to restore config:', e);
        }
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

function fetchJson(url: string): Promise<any> {
    return new Promise((resolve, reject) => {
        http.get(url, (res) => {
            let data = '';
            res.on('data', chunk => data += chunk);
            res.on('end', () => {
                try { resolve(JSON.parse(data)); } catch { resolve(null); }
            });
        }).on('error', reject);
    });
}

function putJson(url: string, body: any): Promise<any> {
    return new Promise((resolve, reject) => {
        const data = JSON.stringify(body);
        const req = http.request(url, {
            method: 'PUT',
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
