import { describe, it, expect, vi, beforeEach } from 'vitest';
import { ExtensionHostChannel } from '../channel';
import {
    CAP_PREVIEW_SERVE,
    CAP_PREVIEW_SERVE_SCRIPTS,
    CAP_PREVIEW_SERVE_NETWORK,
    CAP_PREVIEW_RELEASE,
    CAP_PREVIEW_NAV_STATE,
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

const TEST_TOKEN = 'test-channel-token-0123456789abcdef';

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
        channelToken: TEST_TOKEN,
    });
    // Arm the channel: model the mounted document proving itself, then drop
    // the handshake frames so assertions about posted messages stay index-stable.
    channel.notifyFrameLoad();
    window.dispatchEvent(new MessageEvent('message', {
        data: { type: 'exthello', token: TEST_TOKEN }, source: contentWindow as any, origin: 'null',
    }));
    messages.length = 0;
    return { channel, contentWindow, messages };
}

const deleteCalls = () =>
    apiFetchMock.mock.calls.filter(([, init]) => init?.method === 'DELETE');

function send(_channel: ExtensionHostChannel, contentWindow: any, msg: HostRequest, origin = 'null') {
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
    it('registers the three serve postures, preview.release and preview.navState as call capabilities', () => {
        for (const cap of [CAP_PREVIEW_SERVE, CAP_PREVIEW_SERVE_SCRIPTS, CAP_PREVIEW_SERVE_NETWORK, CAP_PREVIEW_RELEASE, CAP_PREVIEW_NAV_STATE]) {
            expect(SUPPORTED_CAPABILITIES[cap], cap).toBeTruthy();
            expect(SUPPORTED_CAPABILITIES[cap].kind).toBe('call');
            expect(typeof SUPPORTED_CAPABILITIES[cap].handler).toBe('function');
        }
    });

    it('grants only declared, known preview capabilities', () => {
        const granted = computeGrants([CAP_PREVIEW_SERVE, CAP_PREVIEW_SERVE_SCRIPTS, CAP_PREVIEW_SERVE_NETWORK, CAP_PREVIEW_RELEASE, CAP_PREVIEW_NAV_STATE, 'preview.bogus']);
        expect(granted).toContain(CAP_PREVIEW_SERVE);
        expect(granted).toContain(CAP_PREVIEW_SERVE_SCRIPTS);
        expect(granted).toContain(CAP_PREVIEW_SERVE_NETWORK);
        expect(granted).toContain(CAP_PREVIEW_RELEASE);
        expect(granted).toContain(CAP_PREVIEW_NAV_STATE);
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
        expect(body.posture).toBe('no-script');

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
        expect(body).toEqual({ content: '<svg/>', project: 'p2', path: 'page.html', task: '', posture: 'no-script' });
        expect(messages.find((m) => m.id === 'm1')?.ok).toBe(true);
    });

    it('preview.serveScripts is the only capability that posts the scripts posture', async () => {
        okResponse({ token: 'tokS', url: '/api/preview/tokS/' });
        const { channel, contentWindow } = makeChannel([CAP_PREVIEW_SERVE_SCRIPTS], {
            component: 'FileEditor', filePath: 'p.html', projectId: 'p1',
        });

        send(channel, contentWindow, {
            type: 'call', id: 'm1', cap: CAP_PREVIEW_SERVE_SCRIPTS, args: { content: '<html/>' },
        });
        await flush();

        const body = JSON.parse(apiFetchMock.mock.calls[0][1]?.body as string);
        expect(body.posture).toBe('scripts');
    });

    it('preview.serveNetwork is the only capability that serves the permissive CSP', async () => {
        okResponse({ token: 'tokN', url: '/api/preview/tokN/' });
        const { channel, contentWindow } = makeChannel([CAP_PREVIEW_SERVE_NETWORK], {
            component: 'FileEditor', filePath: 'p.html', projectId: 'p1',
        });

        send(channel, contentWindow, {
            type: 'call', id: 'm1', cap: CAP_PREVIEW_SERVE_NETWORK, args: { content: '<html/>' },
        });
        await flush();

        const body = JSON.parse(apiFetchMock.mock.calls[0][1]?.body as string);
        expect(body.posture).toBe('network');
    });

    it('rejects preview.serveNetwork when the extension did not declare it', async () => {
        // Granted only the base postures — the permissive path must be
        // unreachable even for a known extension.
        const { channel, contentWindow, messages } = makeChannel(
            [CAP_PREVIEW_SERVE, CAP_PREVIEW_SERVE_SCRIPTS], {},
        );

        send(channel, contentWindow, {
            type: 'call', id: 'm1', cap: CAP_PREVIEW_SERVE_NETWORK, args: { content: '<html/>' },
        });
        await flush();

        const resp = messages.find((m) => m.id === 'm1');
        expect(resp.ok).toBe(false);
        expect(resp.error).toContain('not granted');
        expect(apiFetchMock).not.toHaveBeenCalled();
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

describe('preview.navState', () => {
    beforeEach(() => { apiFetchMock.mockReset(); });

    it('GETs the token nav endpoint and passes the record through', async () => {
        okResponse({ seq: 2, path: 'docs/page2.html', ok: true, isDoc: false });
        const { channel, contentWindow, messages } = makeChannel([CAP_PREVIEW_NAV_STATE], {});

        send(channel, contentWindow, {
            type: 'call', id: 'm1', cap: CAP_PREVIEW_NAV_STATE, args: { token: 'tok123' },
        });
        await flush();

        expect(apiFetchMock).toHaveBeenCalledTimes(1);
        const [url, init] = apiFetchMock.mock.calls[0];
        expect(url).toBe('/api/preview-nav/tok123');
        expect(init?.method).toBeUndefined(); // GET
        const resp = messages.find((m) => m.id === 'm1');
        expect(resp.ok).toBe(true);
        expect(resp.data).toEqual({ seq: 2, path: 'docs/page2.html', ok: true, isDoc: false });
    });

    it('throws on non-ok (unknown token → the extension treats it as an escape)', async () => {
        failResponse(404);
        const { channel, contentWindow, messages } = makeChannel([CAP_PREVIEW_NAV_STATE], {});

        send(channel, contentWindow, {
            type: 'call', id: 'm1', cap: CAP_PREVIEW_NAV_STATE, args: { token: 'gone' },
        });
        await flush();

        const resp = messages.find((m) => m.id === 'm1');
        expect(resp.ok).toBe(false);
        expect(resp.error).toContain('404');
    });

    it('rejects the call when not granted', async () => {
        const { channel, contentWindow, messages } = makeChannel([], {});
        send(channel, contentWindow, {
            type: 'call', id: 'm1', cap: CAP_PREVIEW_NAV_STATE, args: { token: 'tok123' },
        });
        await flush();
        const resp = messages.find((m) => m.id === 'm1');
        expect(resp.ok).toBe(false);
        expect(resp.error).toContain('not granted');
        expect(apiFetchMock).not.toHaveBeenCalled();
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
        apiFetchMock.mockImplementation(async (_url: string, init?: any) => {
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
        apiFetchMock.mockImplementation(async (_url: string, init?: any) => {
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
        apiFetchMock.mockImplementation((_url: string, init?: any) => {
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
        apiFetchMock.mockImplementation(async (_url: string, init?: any) => {
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
        apiFetchMock.mockImplementation(async (_url: string, init?: any) => {
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
