/**
 * The bytes reply path of the host channel.
 *
 * `post()` round-trips through JSON to strip Svelte `$state` proxies, and a Blob run through it
 * arrives as `{}` — the extension would render an empty file and be told nothing went wrong. These
 * tests pin the other half: bytes leave as a structured clone, and everything that gates a normal
 * post (arming, the grant, the budget) still gates them.
 */
import { describe, it, expect, afterEach, vi } from 'vitest';
import { ExtensionHostChannel } from '../channel';
import { SUPPORTED_CAPABILITIES } from '../capabilities';
import type { HostRequest } from '../types';

const TEST_TOKEN = 'bytes-channel-token-0123456789ab';
const FAKE_CAP = 'test.getBytes';
const FILE_BYTES = new Uint8Array([0x00, 0x01, 'M'.charCodeAt(0), 'I'.charCodeAt(0), 0xff, 0x80]);

function makeFakeIframe() {
    const messages: any[] = [];
    const contentWindow = {
        postMessage: (msg: any) => { messages.push(msg); },
    };
    const iframe: any = { contentWindow, style: {} as CSSStyleDeclaration, remove: vi.fn() };
    return { iframe, contentWindow, messages };
}

function makeChannel(granted: string[]) {
    const { iframe, contentWindow, messages } = makeFakeIframe();
    const channel = new ExtensionHostChannel({
        extensionId: 'ext-1',
        extensionName: 'BytesTest',
        point: 'fileEditor' as any,
        granted,
        config: {},
        context: { component: 'FileEditor', filePath: '/repo/part.zbin', projectId: 'p1' },
        anchor: null,
        iframe,
        channelToken: TEST_TOKEN,
    });
    const arm = () => {
        channel.notifyFrameLoad();
        send(contentWindow, { type: 'exthello', token: TEST_TOKEN });
    };
    return { channel, contentWindow, messages, arm };
}

function send(contentWindow: any, msg: HostRequest | { type: string; [k: string]: any }) {
    window.dispatchEvent(new MessageEvent('message', { data: msg, source: contentWindow as any, origin: 'null' }));
}

function call(cap: string, id = 'c1'): HostRequest {
    return { type: 'call', id, cap, args: {} };
}

/** Let the flush of a rejected promise and the posted reply land. */
const settled = () => new Promise((resolve) => setTimeout(resolve, 0));

function declareHandler(handler: () => any) {
    SUPPORTED_CAPABILITIES[FAKE_CAP] = { kind: 'call', handler };
}

afterEach(() => {
    delete SUPPORTED_CAPABILITIES[FAKE_CAP];
});

describe('the bytes reply', () => {
    it('carries the file itself, not what JSON makes of it', async () => {
        const blob = new Blob([FILE_BYTES], { type: 'application/octet-stream' });
        declareHandler(() => blob);
        const { channel, contentWindow, messages, arm } = makeChannel([FAKE_CAP]);
        arm();

        send(contentWindow, call(FAKE_CAP));
        await settled();

        const got = messages.find((m) => m.type === 'bytes');
        expect(got, 'a bytes reply was never posted').toBeTruthy();
        expect(got.id).toBe('c1');
        expect(got.ok).toBe(true);
        expect(got.byteLength).toBe(FILE_BYTES.length);
        // The reference, not a copy of its enumerable properties: `{}` is what stringify leaves.
        expect(got.blob).toBe(blob);
        expect(got.blob.size).toBe(FILE_BYTES.length);
        expect(messages.some((m) => m.type === 'response' && m.id === 'c1')).toBe(false);
        channel.destroy();
    });

    it('reaches the frame with the bytes unmodified', async () => {
        const blob = new Blob([FILE_BYTES], { type: 'application/octet-stream' });
        declareHandler(() => blob);
        const { channel, contentWindow, messages, arm } = makeChannel([FAKE_CAP]);
        arm();

        send(contentWindow, call(FAKE_CAP));
        await settled();

        const got = messages.find((m) => m.type === 'bytes');
        const read = new Uint8Array(await got.blob.arrayBuffer());
        expect(Array.from(read)).toEqual(Array.from(FILE_BYTES));
        channel.destroy();
    });

    it('keeps a value reply on the JSON path, where the proxies are stripped', async () => {
        declareHandler(() => ({ content: 'text' }));
        const { channel, contentWindow, messages, arm } = makeChannel([FAKE_CAP]);
        arm();

        send(contentWindow, call(FAKE_CAP));
        await settled();

        expect(messages.find((m) => m.type === 'bytes')).toBeUndefined();
        const got = messages.find((m) => m.type === 'response' && m.id === 'c1');
        expect(got?.ok).toBe(true);
        expect(got?.data).toEqual({ content: 'text' });
        channel.destroy();
    });

    it('posts nothing to a frame that never handshook', async () => {
        const blob = new Blob([FILE_BYTES]);
        declareHandler(() => blob);
        const { channel, contentWindow, messages } = makeChannel([FAKE_CAP]);
        // No arm(): the document has not echoed the mount token.

        send(contentWindow, call(FAKE_CAP));
        await settled();

        expect(messages.some((m) => m.type === 'bytes')).toBe(false);
        channel.destroy();
    });

    it('withholds the file from an extension the capability was not granted to', async () => {
        const blob = new Blob([FILE_BYTES]);
        declareHandler(() => blob);
        const { channel, contentWindow, messages, arm } = makeChannel(['config.get']);
        arm();

        send(contentWindow, call(FAKE_CAP));
        await settled();

        expect(messages.some((m) => m.type === 'bytes')).toBe(false);
        const got = messages.find((m) => m.type === 'response' && m.id === 'c1');
        expect(got?.ok).toBe(false);
        expect(got?.error).toContain('not granted');
        channel.destroy();
    });

    it('answers a handler that throws with the refusal every other call gets', async () => {
        declareHandler(() => { throw new Error('no version of this file exists'); });
        const { channel, contentWindow, messages, arm } = makeChannel([FAKE_CAP]);
        arm();

        send(contentWindow, call(FAKE_CAP));
        await settled();

        expect(messages.some((m) => m.type === 'bytes')).toBe(false);
        const got = messages.find((m) => m.type === 'response' && m.id === 'c1');
        expect(got?.ok).toBe(false);
        expect(got?.error).toContain('no version of this file exists');
        channel.destroy();
    });
});
