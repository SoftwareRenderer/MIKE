import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import type { Project } from '$lib/types';

/**
 * The write a settings screen ends on: `updateProject` puts the edits on the project's own route and hands
 * back the row the server answered with. The screens that call it are measured by their own files against a
 * mock of this function, and the server's storing of the same fields is
 * `TestProjectSandboxListSettingsRoundTrip` — what nothing else would pin is the request in between: which
 * route carries the edits, and whose row the caller ends up holding.
 */

const ROW = { id: 'p1', name: 'a project', path: '/srv/a' } as Project;

let calls: any[][];

beforeEach(() => {
    calls = [];
    vi.stubGlobal('fetch', vi.fn(async (url: any, init: any) => {
        // A cold session costs the CSRF bootstrap its own request; only what follows it is under test.
        if (String(url).includes('/api/auth/session')) {
            return { ok: true, status: 200, headers: new Headers(), json: async () => ({ authenticated: true, csrf_token: 'csrf-token' }) };
        }
        calls.push([url, init]);
        return { ok: true, status: 200, headers: new Headers(), json: async () => ROW };
    }));
});

afterEach(() => {
    vi.unstubAllGlobals();
});

describe('updateProject', () => {
    it('puts the edits on the project route and answers with the row the server wrote', async () => {
        const { updateProject } = await import('$lib/api/projects');
        const saved = await updateProject('p1', { sandbox_envs: [{ key: 'DB_HOST', value: 'localhost' }] } as any);

        expect(saved).toEqual(ROW);
        expect(calls).toHaveLength(1);
        const [url, init] = calls[0];
        expect(url).toBe('/api/projects/p1');
        expect(init.method).toBe('PUT');
        expect(JSON.parse(init.body)).toEqual({ sandbox_envs: [{ key: 'DB_HOST', value: 'localhost' }] });
        expect(new Headers(init.headers).get('content-type')).toBe('application/json');
    });

    it('refuses by the server\'s own sentence, so a settings dialog can put it on the field', async () => {
        (globalThis as any).fetch = vi.fn(async (url: any) => {
            if (String(url).includes('/api/auth/session')) {
                return { ok: true, status: 200, headers: new Headers(), json: async () => ({ authenticated: true, csrf_token: 'csrf-token' }) };
            }
            return { ok: false, status: 400, headers: new Headers(), json: async () => ({ error: 'the path cannot be read' }) };
        });

        const { updateProject } = await import('$lib/api/projects');
        await expect(updateProject('p1', { sandbox_mounts: [] } as any)).rejects.toThrow(/the path cannot be read/);
    });
});
