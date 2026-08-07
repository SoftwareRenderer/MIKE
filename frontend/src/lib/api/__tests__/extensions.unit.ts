import { describe, it, expect, vi, beforeEach } from 'vitest';

// Mock fetch globally
let mockFetch: ReturnType<typeof vi.fn>;
beforeEach(() => {
    mockFetch = vi.fn();
    (globalThis as any).fetch = mockFetch;
});

// Helper to find API calls (excluding CSRF session)
function apiCalls(calls: any[][]): any[][] {
    return calls.filter((c) => typeof c[0] === 'string' && !c[0].includes('/api/auth/session'));
}

// Helper to mock a successful API response
function mockSuccess(data: any, status = 200) {
    mockFetch.mockImplementation(async (url: any) => {
        if (typeof url === 'string' && url.includes('/api/auth/session')) {
            return {
                ok: true,
                status: 200,
                json: async () => ({ authenticated: true, csrf_token: 'test-token' }),
            };
        }
        return {
            ok: true,
            status,
            json: async () => data,
            text: async () => JSON.stringify(data),
        };
    });
}

function mockError(message: string, status = 400) {
    mockFetch.mockImplementation(async (url: any) => {
        if (typeof url === 'string' && url.includes('/api/auth/session')) {
            return {
                ok: true,
                status: 200,
                json: async () => ({ authenticated: true, csrf_token: 'test-token' }),
            };
        }
        return {
            ok: false,
            status,
            json: async () => ({ error: message }),
        };
    });
}

describe('extensions API', () => {
    it('listExtensions returns array of extensions', async () => {
        const extensions = [
            { id: 'gopls-1', name: 'gopls', type: 'lsp', enabled: true, running: false },
            { id: 'ts-1', name: 'typescript', type: 'lsp', enabled: true, running: true },
        ];
        mockSuccess(extensions);

        const { listExtensions } = await import('$lib/api/extensions');
        const result = await listExtensions();

        expect(result).toEqual(extensions);
        const calls = apiCalls(mockFetch.mock.calls);
        expect(calls[0][0]).toContain('/api/extensions');
    });

    it('getExtension returns single extension', async () => {
        const ext = { id: 'gopls-1', name: 'gopls', type: 'lsp', enabled: true };
        mockSuccess(ext);

        const { getExtension } = await import('$lib/api/extensions');
        const result = await getExtension('gopls-1');

        expect(result).toEqual(ext);
        const calls = apiCalls(mockFetch.mock.calls);
        expect(calls[0][0]).toContain('/api/extensions/gopls-1');
    });

    it('updateExtension sends PUT with correct body', async () => {
        const data = { version: '2.0.0' };
        const result = { id: 'gopls-1', name: 'gopls', version: '2.0.0', type: 'lsp', enabled: true };
        mockSuccess(result);

        const { updateExtension } = await import('$lib/api/extensions');
        const res = await updateExtension('gopls-1', data);

        expect(res).toEqual(result);
        const calls = apiCalls(mockFetch.mock.calls);
        expect(calls[0][0]).toContain('/api/extensions/gopls-1');
        expect(calls[0][1].method).toBe('PUT');
        expect(JSON.parse(calls[0][1].body)).toEqual(data);
    });

    it('deleteExtension sends DELETE', async () => {
        mockSuccess({ status: 'deleted' });

        const { deleteExtension } = await import('$lib/api/extensions');
        await deleteExtension('gopls-1');

        const calls = apiCalls(mockFetch.mock.calls);
        expect(calls[0][0]).toContain('/api/extensions/gopls-1');
        expect(calls[0][1].method).toBe('DELETE');
    });

    it('setExtensionEnabled sends PATCH with enabled state', async () => {
        const result = { id: 'gopls-1', name: 'gopls', enabled: false, type: 'lsp' };
        mockSuccess(result);

        const { setExtensionEnabled } = await import('$lib/api/extensions');
        const res = await setExtensionEnabled('gopls-1', false);

        expect(res).toEqual(result);
        const calls = apiCalls(mockFetch.mock.calls);
        expect(calls[0][0]).toContain('/api/extensions/gopls-1/enabled');
        expect(calls[0][1].method).toBe('PATCH');
        expect(JSON.parse(calls[0][1].body)).toEqual({ enabled: false });
    });

    it('getRegistryIndex returns registry entries', async () => {
        const entries = [
            { name: 'gopls', version: '1.0.0', type: 'lsp', display_name: 'Go LSP', description: 'Go', author: 'Go Team' },
        ];
        mockSuccess(entries);

        const { getRegistryIndex } = await import('$lib/api/extensions');
        const result = await getRegistryIndex();

        expect(result).toEqual(entries);
        const calls = apiCalls(mockFetch.mock.calls);
        expect(calls[0][0]).toContain('/api/extensions/registry');
    });

    it('getRegistryIndex with custom URL', async () => {
        mockSuccess([]);

        const { getRegistryIndex } = await import('$lib/api/extensions');
        await getRegistryIndex('https://custom.registry/index.json');

        const calls = apiCalls(mockFetch.mock.calls);
        expect(calls[0][0]).toContain('url=https%3A%2F%2Fcustom.registry%2Findex.json');
    });

    it('installExtension streams SSE progress then final result', async () => {
        const result = { id: 'gopls-1', name: 'gopls', type: 'lsp', enabled: true, install_source: 'registry' };
        const chunks = [
            'data: ' + JSON.stringify({ extension: 'gopls', progress: 30, message: 'Building image...', done: false }) + '\n\n',
            'data: ' + JSON.stringify({ extension: 'gopls', progress: 100, message: 'Installed', done: true, result }) + '\n\n',
        ];
        const encoder = new TextEncoder();
        const body = new ReadableStream<Uint8Array>({
            start(controller) {
                for (const c of chunks) controller.enqueue(encoder.encode(c));
                controller.close();
            },
        });

        mockFetch.mockImplementation(async (url: any) => {
            if (typeof url === 'string' && url.includes('/api/auth/session')) {
                return { ok: true, status: 200, json: async () => ({ authenticated: true, csrf_token: 'test-token' }) };
            }
            return { ok: true, status: 200, body };
        });

        const { installExtension } = await import('$lib/api/extensions');
        const events = [];
        for await (const ev of installExtension('gopls')) {
            events.push(ev);
        }

        expect(events).toHaveLength(2);
        expect(events[0]).toMatchObject({ progress: 30, done: false });
        expect(events[1]).toMatchObject({ done: true, result });

        const calls = apiCalls(mockFetch.mock.calls);
        const installCall = calls.find((c) => String(c[0]).includes('/api/extensions/install'));
        expect(installCall).toBeTruthy();
        expect(installCall![1].method).toBe('POST');
        expect(JSON.parse(installCall![1].body)).toHaveProperty('name', 'gopls');
    });

    it('installExtension throws on HTTP error status', async () => {
        mockError('install already in progress: gopls', 409);

        const { installExtension } = await import('$lib/api/extensions');
        const collect = async () => {
            for await (const _ of installExtension('gopls')) { /* noop */ }
        };
        await expect(collect()).rejects.toThrow('install already in progress: gopls');
    });

    it('checkUpdates returns available updates', async () => {
        const updates = [
            { id: 'gopls-1', name: 'gopls', current_version: '1.0.0', available_version: '2.0.0' },
        ];
        mockSuccess(updates);

        const { checkUpdates } = await import('$lib/api/extensions');
        const result = await checkUpdates();

        expect(result).toEqual(updates);
        const calls = apiCalls(mockFetch.mock.calls);
        expect(calls[0][0]).toContain('/api/extensions/check-updates');
    });

    it('getExtensionLogs returns log text', async () => {
        const logs = '2024/01/01 00:00:00 info: starting gopls\n';
        mockFetch.mockImplementation(async (url: any) => {
            if (typeof url === 'string' && url.includes('/api/auth/session')) {
                return {
                    ok: true,
                    status: 200,
                    json: async () => ({ authenticated: true, csrf_token: 'test-token' }),
                };
            }
            return {
                ok: true,
                status: 200,
                text: async () => logs,
            };
        });

        const { getExtensionLogs } = await import('$lib/api/extensions');
        const result = await getExtensionLogs('gopls-1');

        expect(result).toBe(logs);
        const calls = apiCalls(mockFetch.mock.calls);
        expect(calls[0][0]).toContain('/api/extensions/gopls-1/logs');
    });

    it('throws error on API failure', async () => {
        mockError('extension not found', 404);

        const { getExtension } = await import('$lib/api/extensions');
        await expect(getExtension('nonexistent')).rejects.toThrow();
    });
});
