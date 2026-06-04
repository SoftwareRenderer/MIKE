/**
 * Test setup helper for mock LLM endpoint.
 * 
 * Starts the mock LLM server, points the kanban config to it,
 * and provides cleanup to restore the original config.
 */

import { ChildProcess } from 'node:child_process';
import { execSync } from 'node:child_process';
import { test as base, type APIResponse } from '@playwright/test';

const MOCK_PORT = 9999;
const KANBAN_BASE = 'http://localhost:3000';

interface MockFixtures {
    mockLlmUrl: string;
    stopMockServer: () => void;
}

export const test = base.extend<MockFixtures>({
    mockLlmUrl: async ({}, use) => {
        await use(`http://localhost:${MOCK_PORT}/v1`);
    },
    stopMockServer: async ({}, use) => {
        await use(() => {});
    },
});

let mockServer: ChildProcess | null = null;
let originalConfig: { llama_server_url: string } | null = null;
let mockStarted = false;

/**
 * Start the mock LLM server and update kanban config.
 * Call this once before running tests that need the mock.
 */
export async function setupMockLlm(): Promise<void> {
    if (mockStarted) return;
    mockStarted = true;

    // Save original config
    try {
        const configResp = execSync(`curl -s ${KANBAN_BASE}/api/config`, { encoding: 'utf8' });
        originalConfig = JSON.parse(configResp);
    } catch {
        // Server might not be running, that's ok for local dev
    }

    // Start mock server
    mockServer = startMockServer();
    await waitForMockServer();

    // Update kanban config to point to mock
    if (originalConfig) {
        try {
            execSync(
                `curl -s -X PUT ${KANBAN_BASE}/api/config -H 'Content-Type: application/json' -d '{"llama_server_url":"http://localhost:${MOCK_PORT}/v1","model_name":"mock-model"}'`,
                { encoding: 'utf8' }
            );
        } catch (e) {
            console.error('Failed to update kanban config:', e);
        }
    }
}

/**
 * Restore original kanban config and stop mock server.
 */
export async function teardownMockLlm(): Promise<void> {
    if (!mockStarted) return;

    // Restore original config
    if (originalConfig?.llama_server_url) {
        try {
            execSync(
                `curl -s -X PUT ${KANBAN_BASE}/api/config -H 'Content-Type: application/json' -d '{"llama_server_url":"${originalConfig.llama_server_url}","model_name":"${originalConfig.model_name || 'Qwen3.6-35B-A3B'}"}'`,
                { encoding: 'utf8' }
            );
        } catch (e) {
            console.error('Failed to restore kanban config:', e);
        }
    }

    // Stop mock server
    stopMockServer();
    mockStarted = false;
}

function startMockServer(): ChildProcess {
    const { spawn } = await import('node:child_process');
    const server = spawn('node', ['frontend/tests/mock-llm.js', MOCK_PORT.toString()], {
        cwd: '/home/mwren/projects/kanban',
        stdio: ['ignore', 'pipe', 'pipe'],
        detached: false,
    });

    server.stderr.on('data', (data) => {
        // Silently consume stderr (startup messages)
    });

    server.on('error', (err) => {
        console.error('Mock server error:', err);
    });

    return server;
}

function stopMockServer(): void {
    if (mockServer) {
        mockServer.kill('SIGTERM');
        mockServer = null;
    }
}

async function waitForMockServer(timeout = 5000): Promise<void> {
    const start = Date.now();
    while (Date.now() - start < timeout) {
        try {
            const { request } = await import('node:http');
            await new Promise<void>((resolve, reject) => {
                const req = request(`http://localhost:${MOCK_PORT}/health`, { method: 'GET' }, (res) => {
                    // Any response means server is up
                    resolve();
                });
                req.on('error', () => reject(new Error('Not ready')));
                req.end();
            });
            return;
        } catch {
            await new Promise(r => setTimeout(r, 100));
        }
    }
    throw new Error(`Mock LLM server did not start within ${timeout}ms`);
}
