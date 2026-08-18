import { describe, it, expect, vi, beforeEach } from 'vitest';
import { ExtensionHostChannel } from '../channel';
import {
    CAP_PREVIEW_SERVE,
    CAP_PREVIEW_RELEASE,
    SUPPORTED_CAPABILITIES,
    computeGrants,
} from '../capabilities';
import { apiFetch } from '$lib/api/helpers';
import type { HostRequest } from '../types';

vi.mock('$lib/api/helpers', async (importOriginal) => {
    const actual = await importOriginal<typeof import('$lib/api/helpers')>();
    return { ...actual, apiFetch: vi.fn() };
});

const apiFetchMock = vi.mocked(apiFetch);

function makeChannel(granted: string[], context: any, anchor: HTMLElement | null = null) {
    const messages: any[] = [];
    const contentWindow = { postMessage: (msg: any) => messages.push(msg) };
    const iframe: any = { contentWindow, style: {} as CSSStyleDeclaration, remove: vi.fn() };
    const channel = new ExtensionHostChannel({
        extensionId: 'html-preview',
        extensionName: 'html-preview',
        point: 'fileEditor' as any,
        granted,
        config: {},
        context,
        anchor,
        iframe,
    });
    return { channel, contentWindow, messages };
}

const deleteCalls = () =>
    apiFetchMock.mock.calls.filter(([url, init]) => init?.method === 'DELETE');

function send(channel: ExtensionHostChannel, contentWindow: any, msg: HostRequest, origin = 'null') {
    window.dispatchEvent(new MessageEvent('message', { data: msg, source: contentWindow as any, origin }));
}

const flush = () => new Promise((r) => setTimeout(r, 0));

function okResponse(data: any) {
    apiFetchMock.mockResolvedValue({ ok: true, status: 200, json: async () => data } as any);
}

function failResponse(status: number) {
    apiFetchMock.mockResolvedValue({ ok: false, status, json: async () => ({}) } as any);
}

describe('preview capability catalog', () => {
    it('registers preview.serve and preview.release as call capabilities', () => {
        expect(SUPPORTED_CAPABILITIES[CAP_PREVIEW_SERVE]).toBeTruthy();
        expect(SUPPORTED_CAPABILITIES[CAP_PREVIEW_SERVE].kind).toBe('call');
        expect(typeof SUPPORTED_CAPABILITIES[CAP_PREVIEW_SERVE].handler).toBe('function');
        expect(SUPPORTED_CAPABILITIES[CAP_PREVIEW_RELEASE]).toBeTruthy();
        expect(SUPPORTED_CAPABILITIES[CAP_PREVIEW_RELEASE].kind).toBe('call');
        expect(typeof SUPPORTED_CAPABILITIES[CAP_PREVIEW_RELEASE].handler).toBe('function');
    });

    it('grants only declared, known preview capabilities', () => {
        const granted = computeGrants([CAP_PREVIEW_SERVE, CAP_PREVIEW_RELEASE, 'preview.bogus']);
        expect(granted).toContain(CAP_PREVIEW_SERVE);
        expect(granted).toContain(CAP_PREVIEW_RELEASE);
        expect(granted).not.toContain('preview.bogus');
    });
});

describe('preview.serve', () => {
    beforeEach(() => { apiFetchMock.mockReset(); });

    it('serves the content with project/path/task taken from the host context only', async () => {
        okResponse({ token: 'tok123', url: '/api/preview/tok123/' });
        const { channel, contentWindow, messages } = makeChannel([CAP_PREVIEW_SERVE], {
            component: 'FileEditor',
            filePath: '/proj/page.html',
            projectId: 'p1',
            taskId: 't1',
        });

        // Extension-supplied project/path/task must be IGNORED — the host
        // fills them from its own injection context.
        send(channel, contentWindow, {
            type: 'call', id: 'm1', cap: CAP_PREVIEW_SERVE,
            args: { content: '<html></html>', project: 'EVIL', path: '/etc/passwd', task: 'EVIL' },
        });
        await flush();

        expect(apiFetchMock).toHaveBeenCalledTimes(1);
        const [url, init] = apiFetchMock.mock.calls[0];
        expect(url).toBe('/api/preview');
        expect(init?.method).toBe('POST');
        const body = JSON.parse(init?.body as string);
        expect(body.content).toBe('<html></html>');
        expect(body.project).toBe('p1');
        expect(body.path).toBe('/proj/page.html');
        expect(body.task).toBe('t1');

        const resp = messages.find((m) => m.type === 'response' && m.id === 'm1');
        expect(resp.ok).toBe(true);
        expect(resp.data).toEqual({ token: 'tok123', url: '/api/preview/tok123/' });
    });

    it('works without task/project context fields (self-contained serve)', async () => {
        okResponse({ token: 'tok456', url: '/api/preview/tok456/' });
        const { channel, contentWindow, messages } = makeChannel([CAP_PREVIEW_SERVE], {
            component: 'FileEditor',
            filePath: 'page.html',
            projectId: 'p2',
        });

        send(channel, contentWindow, {
            type: 'call', id: 'm1', cap: CAP_PREVIEW_SERVE, args: { content: '<svg/>' },
        });
        await flush();

        const body = JSON.parse(apiFetchMock.mock.calls[0][1]?.body as string);
        expect(body).toEqual({ content: '<svg/>', project: 'p2', path: 'page.html', task: '' });
        expect(messages.find((m) => m.id === 'm1')?.ok).toBe(true);
    });

    it('rejects the call when not granted', async () => {
        const { channel, contentWindow, messages } = makeChannel([], {});
        send(channel, contentWindow, {
            type: 'call', id: 'm1', cap: CAP_PREVIEW_SERVE, args: { content: '<html/>' },
        });
        await flush();
        const resp = messages.find((m) => m.type === 'response' && m.id === 'm1');
        expect(resp.ok).toBe(false);
        expect(resp.error).toContain('not granted');
        expect(apiFetchMock).not.toHaveBeenCalled();
    });

    it('surfaces server errors', async () => {
        failResponse(400);
        const { channel, contentWindow, messages } = makeChannel([CAP_PREVIEW_SERVE], {
            component: 'FileEditor', filePath: 'p.html', projectId: 'p1',
        });
        send(channel, contentWindow, {
            type: 'call', id: 'm1', cap: CAP_PREVIEW_SERVE, args: { content: '<html/>' },
        });
        await flush();
        const resp = messages.find((m) => m.type === 'response' && m.id === 'm1');
        expect(resp.ok).toBe(false);
        expect(resp.error).toContain('400');
    });
});

describe('preview.release', () => {
    beforeEach(() => { apiFetchMock.mockReset(); });

    it('deletes the preview token', async () => {
        okResponse({ ok: true });
        const { channel, contentWindow, messages } = makeChannel([CAP_PREVIEW_RELEASE], {});

        send(channel, contentWindow, {
            type: 'call', id: 'm1', cap: CAP_PREVIEW_RELEASE, args: { token: 'tok123' },
        });
        await flush();

        expect(apiFetchMock).toHaveBeenCalledTimes(1);
        const [url, init] = apiFetchMock.mock.calls[0];
        expect(url).toBe('/api/preview/tok123');
        expect(init?.method).toBe('DELETE');
        expect(messages.find((m) => m.id === 'm1')?.ok).toBe(true);
    });

    it('a second serve from the same editor implicitly releases the previous doc', async () => {
        let n = 0;
        apiFetchMock.mockImplementation(async (url: string, init?: any) => {
            if (init?.method === 'DELETE') {
                return { ok: true, status: 200, json: async () => ({ ok: true }) } as any;
            }
            n++;
            return { ok: true, status: 200, json: async () => ({ token: `tok${n}`, url: `/api/preview/tok${n}/` }) } as any;
        });
        const { channel, contentWindow } = makeChannel(
            [CAP_PREVIEW_SERVE, CAP_PREVIEW_RELEASE], {}, document.createElement('div'),
        );

        send(channel, contentWindow, { type: 'call', id: 'm1', cap: CAP_PREVIEW_SERVE, args: { content: 'a' } });
        await flush();
        send(channel, contentWindow, { type: 'call', id: 'm2', cap: CAP_PREVIEW_SERVE, args: { content: 'b' } });
        await flush();

        const deletes = deleteCalls();
        expect(deletes).toHaveLength(1);
        expect(deletes[0][0]).toBe('/api/preview/tok1');
    });

    it('explicit release clears implicit tracking (no duplicate release)', async () => {
        let n = 0;
        apiFetchMock.mockImplementation(async (url: string, init?: any) => {
            if (init?.method === 'DELETE') {
                return { ok: true, status: 200, json: async () => ({ ok: true }) } as any;
            }
            n++;
            return { ok: true, status: 200, json: async () => ({ token: `tok${n}`, url: `/api/preview/tok${n}/` }) } as any;
        });
        const { channel, contentWindow } = makeChannel(
            [CAP_PREVIEW_SERVE, CAP_PREVIEW_RELEASE], {}, document.createElement('div'),
        );

        send(channel, contentWindow, { type: 'call', id: 'm1', cap: CAP_PREVIEW_SERVE, args: { content: 'a' } });
        await flush();
        send(channel, contentWindow, { type: 'call', id: 'm2', cap: CAP_PREVIEW_RELEASE, args: { token: 'tok1' } });
        await flush();
        send(channel, contentWindow, { type: 'call', id: 'm3', cap: CAP_PREVIEW_SERVE, args: { content: 'b' } });
        await flush();

        // Only the explicit release hit the server — the second serve had no
        // tracked doc to supersede.
        expect(deleteCalls()).toHaveLength(1);
    });

    it('a stale serve completion does not release the doc a newer serve installed', async () => {
        // Two overlapping serves: the OLDER one completes LAST. Without
        // generation ordering it would release the NEWER serve's doc.
        const postResolvers: Array<(v: any) => void> = [];
        apiFetchMock.mockImplementation((url: string, init?: any) => {
            if (init?.method === 'DELETE') {
                return Promise.resolve({ ok: true, status: 200, json: async () => ({ ok: true }) } as any);
            }
            return new Promise((resolve) => postResolvers.push(resolve));
        });
        const { channel, contentWindow } = makeChannel(
            [CAP_PREVIEW_SERVE], {}, document.createElement('div'),
        );

        send(channel, contentWindow, { type: 'call', id: 'm1', cap: CAP_PREVIEW_SERVE, args: { content: 'a' } });
        await flush();
        send(channel, contentWindow, { type: 'call', id: 'm2', cap: CAP_PREVIEW_SERVE, args: { content: 'b' } });
        await flush();

        // Newer serve completes first: no previous doc tracked → no release.
        postResolvers[1]({ ok: true, status: 200, json: async () => ({ token: 'tokB', url: '/api/preview/tokB/' }) });
        await flush();
        expect(deleteCalls()).toHaveLength(0);

        // Older serve completes last: must be a no-op (tokB stays installed).
        postResolvers[0]({ ok: true, status: 200, json: async () => ({ token: 'tokA', url: '/api/preview/tokA/' }) });
        await flush();
        expect(deleteCalls()).toHaveLength(0);

        // A third serve releases exactly the tracked doc (tokB), proving the
        // stale completion did not clobber tracking with tokA.
        apiFetchMock.mockImplementation(async (url: string, init?: any) => {
            if (init?.method === 'DELETE') {
                return { ok: true, status: 200, json: async () => ({ ok: true }) } as any;
            }
            return { ok: true, status: 200, json: async () => ({ token: 'tokC', url: '/api/preview/tokC/' }) } as any;
        });
        send(channel, contentWindow, { type: 'call', id: 'm3', cap: CAP_PREVIEW_SERVE, args: { content: 'c' } });
        await flush();
        expect(deleteCalls()).toHaveLength(1);
        expect(deleteCalls()[0][0]).toBe('/api/preview/tokB');
    });

    it('different editors (anchors) do not cross-release', async () => {
        let n = 0;
        apiFetchMock.mockImplementation(async (url: string, init?: any) => {
            if (init?.method === 'DELETE') {
                return { ok: true, status: 200, json: async () => ({ ok: true }) } as any;
            }
            n++;
            return { ok: true, status: 200, json: async () => ({ token: `tok${n}`, url: `/api/preview/tok${n}/` }) } as any;
        });
        const a = makeChannel([CAP_PREVIEW_SERVE], {}, document.createElement('div'));
        const b = makeChannel([CAP_PREVIEW_SERVE], {}, document.createElement('div'));

        send(a.channel, a.contentWindow, { type: 'call', id: 'm1', cap: CAP_PREVIEW_SERVE, args: { content: 'a' } });
        await flush();
        send(b.channel, b.contentWindow, { type: 'call', id: 'm1', cap: CAP_PREVIEW_SERVE, args: { content: 'b' } });
        await flush();

        expect(deleteCalls()).toHaveLength(0);
    });

    it('rejects the call when not granted', async () => {
        const { channel, contentWindow, messages } = makeChannel([], {});
        send(channel, contentWindow, {
            type: 'call', id: 'm1', cap: CAP_PREVIEW_RELEASE, args: { token: 'tok123' },
        });
        await flush();
        const resp = messages.find((m) => m.type === 'response' && m.id === 'm1');
        expect(resp.ok).toBe(false);
        expect(apiFetchMock).not.toHaveBeenCalled();
    });
});
