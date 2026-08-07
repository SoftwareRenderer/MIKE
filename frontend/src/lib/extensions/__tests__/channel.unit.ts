import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { ExtensionHostChannel } from '../channel';
import {
    CAP_CONFIG_GET,
    CAP_CREATE_TASK_INSERT_TEXT,
    CAP_CREATE_TASK_PASTE,
    CAP_NETWORK_PROXY,
    CAP_UI_STATUS,
    CAP_UI_SET_SIZE,
    CAP_STORAGE_SET,
    CAP_LOG_WRITE,
    CAP_EDITOR_ADD_FOOTER_BUTTON,
    CAP_EDITOR_REMOVE_FOOTER_BUTTON,
    CAP_THEME_GET,
} from '../capabilities';
import type { HostRequest } from '../types';

function makeFakeIframe() {
    const messages: any[] = [];
    const targets: string[] = [];
    const contentWindow = { postMessage: (msg: any, origin?: string) => { messages.push(msg); targets.push(origin ?? ''); } };
    const iframe: any = {
        contentWindow,
        style: {} as CSSStyleDeclaration,
        remove: vi.fn(),
    };
    return { iframe, contentWindow, messages, targets };
}

function makeChannel(granted: string[], config: any = { key: 'value' }, anchor: HTMLElement | null = null) {
    const { iframe, contentWindow, messages, targets } = makeFakeIframe();
    const channel = new ExtensionHostChannel({
        extensionId: 'ext-1',
        extensionName: 'Test',
        point: 'createTask' as any,
        granted,
        config,
        context: { component: 'CreateTaskModal' },
        anchor,
        iframe,
    });
    return { channel, contentWindow, messages, targets, iframe };
}

function send(channel: ExtensionHostChannel, contentWindow: any, msg: HostRequest, origin = 'null') {
    window.dispatchEvent(new MessageEvent('message', { data: msg, source: contentWindow as any, origin }));
}

// Helper: await microtasks so async handlers resolve and post responses.
const flush = () => new Promise((r) => setTimeout(r, 0));

describe('ExtensionHostChannel capability enforcement', () => {
    beforeEach(() => {
        document.body.innerHTML = '';
    });

    it('sends an init message with the granted capability set', () => {
        const { channel, messages } = makeChannel([CAP_CONFIG_GET]);
        channel.start();
        const init = messages[0];
        expect(init.type).toBe('init');
        // config.get, log.write, and theme.get are implicit grants added by the channel.
        expect(init.granted).toEqual([CAP_CONFIG_GET, CAP_LOG_WRITE, CAP_THEME_GET]);
        expect(init.config).toEqual({ key: 'value' });
        channel.destroy();
    });

    it('posts to the iframe with a wildcard target (origin enforced on receive)', () => {
        const { channel, targets } = makeChannel([CAP_CONFIG_GET]);
        channel.start();
        // The iframe's opaque origin is not reliably accepted as a targetOrigin
        // by Chromium, so the host uses '*' and instead enforces the opaque
        // origin on the receive side (see the non-opaque-origin rejection test).
        expect(targets.length).toBeGreaterThan(0);
        expect(targets.every((t) => t === '*')).toBe(true);
        channel.destroy();
    });

    it('grants editor.removeFooterButton implicitly when addFooterButton is granted', async () => {
        // Removing a footer button is intrinsic to adding one — granting
        // addFooterButton must also grant removeFooterButton (scoped to the
        // extension's own buttons), even if not declared.
        const { channel, contentWindow, messages } = makeChannel([CAP_EDITOR_ADD_FOOTER_BUTTON]);
        channel.start();
        const init = messages[0];
        expect(init.granted).toContain(CAP_EDITOR_REMOVE_FOOTER_BUTTON);
        // The call is accepted (not rejected as 'not granted'); it resolves ok
        // as a no-op here because no button exists in jsdom.
        send(channel, contentWindow, { type: 'call', id: 'rm', cap: CAP_EDITOR_REMOVE_FOOTER_BUTTON, args: { id: 'x' } });
        await flush();
        const resp = messages[messages.length - 1];
        expect(resp.ok).toBe(true);
        expect(resp.error).toBeUndefined();
        channel.destroy();
    });

    it('does not grant editor.removeFooterButton without addFooterButton', () => {
        const { channel, messages } = makeChannel([CAP_CONFIG_GET]);
        channel.start();
        expect(messages[0].granted).not.toContain(CAP_EDITOR_REMOVE_FOOTER_BUTTON);
        channel.destroy();
    });

    it('handles a call for a granted capability and responds with data', async () => {
        const { channel, contentWindow, messages } = makeChannel([CAP_CONFIG_GET]);
        channel.start();
        send(channel, contentWindow, { type: 'call', id: 'c1', cap: CAP_CONFIG_GET, args: {} });
        await flush();
        const resp = messages[messages.length - 1];
        expect(resp.type).toBe('response');
        expect(resp.id).toBe('c1');
        expect(resp.ok).toBe(true);
        expect(resp.data).toEqual({ key: 'value' });
        channel.destroy();
    });

    it('rejects a call for a capability that was NOT granted', async () => {
        const { channel, contentWindow, messages } = makeChannel([CAP_CONFIG_GET]);
        channel.start();
        // network.proxy is not granted to this extension.
        send(channel, contentWindow, { type: 'call', id: 'c2', cap: CAP_NETWORK_PROXY, args: { url: 'https://x' } });
        await flush();
        const resp = messages[messages.length - 1];
        expect(resp.type).toBe('response');
        expect(resp.ok).toBe(false);
        expect(resp.error).toContain('not granted');
        channel.destroy();
    });

    it('ignores messages from an unexpected source', async () => {
        const { channel, messages } = makeChannel([CAP_CONFIG_GET]);
        channel.start();
        const before = messages.length;
        // Dispatch with a different source (not the iframe contentWindow).
        window.dispatchEvent(new MessageEvent('message', {
            data: { type: 'call', id: 'c3', cap: CAP_CONFIG_GET, args: {} },
            source: {} as any,
        }));
        expect(messages.length).toBe(before);
        channel.destroy();
    });

    it('ignores non-call message types', async () => {
        const { channel, contentWindow, messages } = makeChannel([CAP_CONFIG_GET]);
        channel.start();
        const before = messages.length;
        window.dispatchEvent(new MessageEvent('message', {
            data: { type: 'garbage', payload: 1 },
            source: contentWindow as any,
            origin: 'null',
        }));
        expect(messages.length).toBe(before);
        channel.destroy();
    });

    it('rejects a message from the right window but a non-opaque origin', async () => {
        const { channel, contentWindow, messages } = makeChannel([CAP_CONFIG_GET]);
        channel.start();
        const before = messages.length;
        // Same window (contentWindow) but a real, non-opaque origin — models a
        // same-frame page that was navigated to a hijacked origin.
        send(channel, contentWindow, { type: 'call', id: 'c9', cap: CAP_CONFIG_GET, args: {} }, 'https://evil.example');
        expect(messages.length).toBe(before);
        channel.destroy();
    });

    it('allows any JSON value for storage.set (schema type `any`)', async () => {
        const { channel, contentWindow, messages } = makeChannel([CAP_STORAGE_SET]);
        channel.start();
        // A string value must NOT be rejected as "invalid args" — storage values
        // may be any JSON. (The handler itself will fail on fetch in jsdom, but
        // that is a different, non-schema error.)
        send(channel, contentWindow, { type: 'call', id: 'cs', cap: CAP_STORAGE_SET, args: { key: 'greeting', value: 'hello' } });
        await flush();
        const resp = messages[messages.length - 1];
        expect(resp.ok).toBe(false);
        expect(resp.error).not.toContain('invalid args');
        channel.destroy();
    });

    it('rejects calls with missing/invalid arguments against the schema', async () => {
        const { channel, contentWindow, messages } = makeChannel([CAP_CREATE_TASK_INSERT_TEXT]);
        channel.start();
        // Missing required arg `text`.
        send(channel, contentWindow, { type: 'call', id: 'c4', cap: CAP_CREATE_TASK_INSERT_TEXT, args: {} });
        await flush();
        const resp = messages[messages.length - 1];
        expect(resp.ok).toBe(false);
        expect(resp.error).toContain('invalid args');
        channel.destroy();
    });

    it('inserts plain text into the task description (createTask handler)', async () => {
        const ta = document.createElement('textarea');
        ta.id = 'task-description';
        ta.value = 'hello';
        ta.setSelectionRange(2, 2);
        document.body.appendChild(ta);

        const { channel, contentWindow, messages } = makeChannel([CAP_CREATE_TASK_INSERT_TEXT]);
        channel.start();
        send(channel, contentWindow, { type: 'call', id: 'c5', cap: CAP_CREATE_TASK_INSERT_TEXT, args: { text: ' X ' } });
        await flush();
        expect(ta.value).toBe('he X llo');
        const resp = messages[messages.length - 1];
        expect(resp.ok).toBe(true);
        channel.destroy();
    });

    it('forwards events to the extension', () => {
        const { channel, messages } = makeChannel([CAP_CONFIG_GET]);
        channel.start();
        channel.forward('createTask.events.paste', { text: 'https://github.com/a/b/issues/1' });
        const ev = messages[messages.length - 1];
        expect(ev.type).toBe('event');
        expect(ev.cap).toBe('createTask.events.paste');
        expect(ev.data.text).toContain('github.com');
        channel.destroy();
    });

    it('paste grant listener prevents default and forwards clipboard text', () => {
        const ta = document.createElement('textarea');
        ta.id = 'task-description';
        document.body.appendChild(ta);

        const { channel, messages } = makeChannel([CAP_CREATE_TASK_PASTE]);
        channel.start(); // onGrant registers the paste listener on the textarea

        const ev = new Event('paste', { bubbles: true, cancelable: true }) as ClipboardEvent;
        let defaultPrevented = false;
        Object.defineProperty(ev, 'clipboardData', {
            value: { getData: () => 'https://github.com/a/b/issues/1' },
        });
        Object.defineProperty(ev, 'preventDefault', {
            value: () => { defaultPrevented = true; },
        });
        ta.dispatchEvent(ev);

        expect(defaultPrevented).toBe(true);
        const forwarded = messages.filter((m) => m.type === 'event');
        expect(forwarded.length).toBe(1);
        expect(forwarded[0].cap).toBe('createTask.events.paste');
        expect(forwarded[0].data.text).toContain('github.com');
        channel.destroy();
    });

    it('paste listener is removed on destroy', () => {
        const ta = document.createElement('textarea');
        ta.id = 'task-description';
        document.body.appendChild(ta);

        const { channel, messages } = makeChannel([CAP_CREATE_TASK_PASTE]);
        channel.start();
        channel.destroy(); // removes the listener via eventCleanups

        const ev = new Event('paste', { bubbles: true, cancelable: true }) as ClipboardEvent;
        Object.defineProperty(ev, 'clipboardData', { value: { getData: () => 'x' } });
        ta.dispatchEvent(ev);

        expect(messages.filter((m) => m.type === 'event').length).toBe(0);
    });

    it('destroy() removes the listener and the iframe, and stops responding', async () => {
        const { channel, contentWindow, messages, iframe } = makeChannel([CAP_CONFIG_GET]);
        channel.start();
        channel.destroy();
        expect(iframe.remove).toHaveBeenCalled();

        const before = messages.length;
        send(channel, contentWindow, { type: 'call', id: 'c6', cap: CAP_CONFIG_GET, args: {} });
        await flush();
        expect(messages.length).toBe(before);
    });
});

describe('ExtensionHostChannel ui.* controls', () => {
    let anchor: HTMLDivElement;

    beforeEach(() => {
        document.body.innerHTML = '';
        anchor = document.createElement('div');
        document.body.appendChild(anchor);
    });

    it('renders a loading indicator into the anchor via ui.status', async () => {
        const { channel, contentWindow } = makeChannel([CAP_UI_STATUS], {}, anchor);
        channel.start();
        send(channel, contentWindow, { type: 'call', id: 'u1', cap: CAP_UI_STATUS, args: { state: 'loading', message: 'Importing…' } });
        await flush();
        const status = anchor.querySelector('[data-ext-status]') as HTMLElement;
        expect(status).toBeTruthy();
        expect(status.style.display).toBe('flex');
        expect(status.textContent).toContain('Importing…');
        channel.destroy();
    });

    it('renders an error and replaces the loading content', async () => {
        const { channel, contentWindow } = makeChannel([CAP_UI_STATUS], {}, anchor);
        channel.start();
        send(channel, contentWindow, { type: 'call', id: 'u1', cap: CAP_UI_STATUS, args: { state: 'loading', message: 'Importing…' } });
        await flush();
        send(channel, contentWindow, { type: 'call', id: 'u2', cap: CAP_UI_STATUS, args: { state: 'error', message: 'Failed to import' } });
        await flush();
        const status = anchor.querySelector('[data-ext-status]') as HTMLElement;
        expect(status.textContent).toContain('Failed to import');
        expect(status.textContent).not.toContain('Importing…');
        channel.destroy();
    });

    it('hides the indicator via ui.status', async () => {
        const { channel, contentWindow } = makeChannel([CAP_UI_STATUS], {}, anchor);
        channel.start();
        send(channel, contentWindow, { type: 'call', id: 'u1', cap: CAP_UI_STATUS, args: { state: 'loading', message: 'Importing…' } });
        await flush();
        send(channel, contentWindow, { type: 'call', id: 'u2', cap: CAP_UI_STATUS, args: { state: 'hidden' } });
        await flush();
        const status = anchor.querySelector('[data-ext-status]') as HTMLElement;
        expect(status.style.display).toBe('none');
        channel.destroy();
    });

    it('sizes the iframe via ui.setSize', async () => {
        const { channel, contentWindow, iframe } = makeChannel([CAP_UI_SET_SIZE], {}, anchor);
        channel.start();
        expect(iframe.style.height).not.toBe('120px');
        send(channel, contentWindow, { type: 'call', id: 'u3', cap: CAP_UI_SET_SIZE, args: { height: 120 } });
        await flush();
        expect(iframe.style.height).toBe('120px');
        channel.destroy();
    });

    it('does not render status when there is no anchor', async () => {
        const { channel, contentWindow, messages } = makeChannel([CAP_UI_STATUS], {});
        channel.start();
        send(channel, contentWindow, { type: 'call', id: 'u4', cap: CAP_UI_STATUS, args: { state: 'loading', message: 'x' } });
        await flush();
        // Should still respond ok, but no DOM was created.
        const resp = messages[messages.length - 1];
        expect(resp.ok).toBe(true);
        expect(document.querySelector('[data-ext-status]')).toBeNull();
        channel.destroy();
    });

    it('rejects an unknown ui.status state', async () => {
        const { channel, contentWindow, messages } = makeChannel([CAP_UI_STATUS], {});
        channel.start();
        send(channel, contentWindow, { type: 'call', id: 'u5', cap: CAP_UI_STATUS, args: { state: 'banana' } });
        await flush();
        const resp = messages[messages.length - 1];
        expect(resp.ok).toBe(false);
        expect(resp.error).toContain('unknown ui.status state');
        channel.destroy();
    });
});
