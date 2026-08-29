import { describe, it, expect, beforeEach, vi } from 'vitest';
import { ExtensionHostChannel } from '../channel';
import {
    CAP_CONFIG_GET,
    CAP_CREATE_TASK_INSERT_TEXT,
    CAP_CREATE_TASK_PASTE,
    CAP_NETWORK_PROXY,
    CAP_UI_STATUS,
    CAP_UI_SET_SIZE,
    CAP_UI_MODAL,
    CAP_STORAGE_SET,
    CAP_LOG_WRITE,
    CAP_EDITOR_ADD_FOOTER_BUTTON,
    CAP_EDITOR_REMOVE_FOOTER_BUTTON,
    CAP_EDITOR_GET_CONTENT,
    CAP_EDITOR_CONTENT_CHANGE,
    CAP_EDITOR_SET_OVERLAY,
    CAP_PREVIEW_SERVE,
    CAP_PREVIEW_SERVE_SCRIPTS,
    CAP_PREVIEW_SERVE_NETWORK,
    CAP_PREVIEW_RELEASE,
    CAP_PREVIEW_NAV_STATE,
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

const TEST_TOKEN = 'test-channel-token-0123456789abcdef';

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
        channelToken: TEST_TOKEN,
    });
    /** Model the real mount: iframe `load` -> bridge `exthello` with the URL token. */
    const arm = () => {
        channel.notifyFrameLoad();
        send(channel, contentWindow, { type: 'exthello', token: TEST_TOKEN } as any);
    };
    return { channel, contentWindow, messages, targets, iframe, arm, token: TEST_TOKEN };
}

/** The init message (a frame load posts `exthello-request` before it). */
function initOf(messages: any[]) {
    return messages.find((m) => m.type === 'init');
}

function send(_channel: ExtensionHostChannel, contentWindow: any, msg: HostRequest | { type: string; [k: string]: any }, origin = 'null') {
    window.dispatchEvent(new MessageEvent('message', { data: msg, source: contentWindow as any, origin }));
}

// Helper: await microtasks so async handlers resolve and post responses.
const flush = () => new Promise((r) => setTimeout(r, 0));

describe('ExtensionHostChannel capability enforcement', () => {
    beforeEach(() => {
        document.body.innerHTML = '';
    });

    it('sends an init message with the granted capability set', () => {
        const {  channel, messages , arm } = makeChannel([CAP_CONFIG_GET]);
        arm();
        const init = initOf(messages);
        expect(init.type).toBe('init');
        // config.get, log.write, theme.get, and ui.status are implicit grants added by the channel.
        expect(init.granted).toEqual([CAP_CONFIG_GET, CAP_LOG_WRITE, CAP_THEME_GET, CAP_UI_STATUS]);
        expect(init.config).toEqual({ key: 'value' });
        channel.destroy();
    });

    it('posts to the iframe with a wildcard target (origin enforced on receive)', () => {
        const {  channel, targets , arm } = makeChannel([CAP_CONFIG_GET]);
        arm();
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
        const {  channel, contentWindow, messages , arm } = makeChannel([CAP_EDITOR_ADD_FOOTER_BUTTON]);
        arm();
        const init = initOf(messages);
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
        const {  channel, messages , arm } = makeChannel([CAP_CONFIG_GET]);
        arm();
        expect(initOf(messages).granted).not.toContain(CAP_EDITOR_REMOVE_FOOTER_BUTTON);
        channel.destroy();
    });

    it('grants ui.status implicitly — no manifest declaration needed', async () => {
        // Loading/error indicators render inside the extension's own anchor —
        // safe by construction, so always granted like config.get/log.write.
        const {  channel, contentWindow, messages , arm } = makeChannel([CAP_CONFIG_GET]);
        arm();
        expect(initOf(messages).granted).toContain(CAP_UI_STATUS);
        send(channel, contentWindow, { type: 'call', id: 'us', cap: CAP_UI_STATUS, args: { state: 'loading', message: 'x' } });
        await flush();
        const resp = messages[messages.length - 1];
        expect(resp.ok).toBe(true);
        channel.destroy();
    });

    it('grants editor.contentChange implicitly when editor.getContent is granted', () => {
        // Reading the file implies seeing its subsequent edits (a live view
        // must stay in sync) — no separate declaration needed.
        const {  channel, messages , arm } = makeChannel([CAP_EDITOR_GET_CONTENT]);
        arm();
        expect(initOf(messages).granted).toContain(CAP_EDITOR_CONTENT_CHANGE);
        channel.destroy();
    });

    it('does not grant editor.contentChange without editor.getContent', () => {
        const {  channel, messages , arm } = makeChannel([CAP_CONFIG_GET]);
        arm();
        expect(initOf(messages).granted).not.toContain(CAP_EDITOR_CONTENT_CHANGE);
        channel.destroy();
    });

    it('grants preview.serve + preview.release implicitly when editor.setOverlay is granted', async () => {
        // A replaced editor view needs a served page to show and a way to
        // close it — the strict-CSP serve and release are implied by
        // setOverlay even when not declared.
        const {  channel, contentWindow, messages , arm } = makeChannel([CAP_EDITOR_SET_OVERLAY]);
        arm();
        const init = initOf(messages);
        expect(init.granted).toContain(CAP_PREVIEW_SERVE);
        expect(init.granted).toContain(CAP_PREVIEW_RELEASE);
        // release is a real call: it passes the grant check (the jsdom fetch
        // behind it fails for an unrelated reason — never 'not granted').
        send(channel, contentWindow, { type: 'call', id: 'pr', cap: CAP_PREVIEW_RELEASE, args: { token: 't' } });
        await flush();
        const resp = messages[messages.length - 1];
        expect(resp.ok).toBe(false);
        expect(resp.error).not.toContain('not granted');
        channel.destroy();
    });

    it('does not grant preview.serve/preview.release without editor.setOverlay', () => {
        const {  channel, messages , arm } = makeChannel([CAP_CONFIG_GET]);
        arm();
        expect(initOf(messages).granted).not.toContain(CAP_PREVIEW_SERVE);
        expect(initOf(messages).granted).not.toContain(CAP_PREVIEW_RELEASE);
        channel.destroy();
    });

    it('never implies preview.serveScripts/preview.serveNetwork — they stay explicit', () => {
        // They relax the preview isolation posture — even a full setOverlay
        // grant must not imply them.
        const {  channel, messages , arm } = makeChannel([CAP_EDITOR_SET_OVERLAY]);
        arm();
        expect(initOf(messages).granted).not.toContain(CAP_PREVIEW_SERVE_SCRIPTS);
        expect(initOf(messages).granted).not.toContain(CAP_PREVIEW_SERVE_NETWORK);
        channel.destroy();
    });

    it('grants preview.navState implicitly with any preview capability', () => {
        // The watchdog's decision channel only reports what the server served
        // under the extension's own tokens, so it rides along with whichever
        // preview capability is granted (declared or implied by setOverlay).
        const declared = makeChannel([CAP_PREVIEW_SERVE_SCRIPTS]);
        declared.arm();
        expect(declared.messages.find((m: any) => m.type === 'init').granted).toContain(CAP_PREVIEW_NAV_STATE);
        declared.channel.destroy();

        const implied = makeChannel([CAP_EDITOR_SET_OVERLAY]);
        implied.arm();
        expect(implied.messages.find((m: any) => m.type === 'init').granted).toContain(CAP_PREVIEW_NAV_STATE);
        implied.channel.destroy();
    });

    it('does not grant preview.navState without any preview capability', () => {
        const {  channel, messages , arm } = makeChannel([CAP_UI_MODAL]);
        arm();
        expect(initOf(messages).granted).not.toContain(CAP_PREVIEW_NAV_STATE);
        channel.destroy();
    });

    it('handles a call for a granted capability and responds with data', async () => {
        const {  channel, contentWindow, messages , arm } = makeChannel([CAP_CONFIG_GET]);
        arm();
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
        const {  channel, contentWindow, messages , arm } = makeChannel([CAP_CONFIG_GET]);
        arm();
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
        const {  channel, messages , arm } = makeChannel([CAP_CONFIG_GET]);
        arm();
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
        const {  channel, contentWindow, messages , arm } = makeChannel([CAP_CONFIG_GET]);
        arm();
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
        const {  channel, contentWindow, messages , arm } = makeChannel([CAP_CONFIG_GET]);
        arm();
        const before = messages.length;
        // Same window (contentWindow) but a real, non-opaque origin — models a
        // same-frame page that was navigated to a hijacked origin.
        send(channel, contentWindow, { type: 'call', id: 'c9', cap: CAP_CONFIG_GET, args: {} }, 'https://evil.example');
        expect(messages.length).toBe(before);
        channel.destroy();
    });

    it('allows any JSON value for storage.set (schema type `any`)', async () => {
        const {  channel, contentWindow, messages , arm } = makeChannel([CAP_STORAGE_SET]);
        arm();
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
        const {  channel, contentWindow, messages , arm } = makeChannel([CAP_CREATE_TASK_INSERT_TEXT]);
        arm();
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

        const {  channel, contentWindow, messages , arm } = makeChannel([CAP_CREATE_TASK_INSERT_TEXT]);
        arm();
        send(channel, contentWindow, { type: 'call', id: 'c5', cap: CAP_CREATE_TASK_INSERT_TEXT, args: { text: ' X ' } });
        await flush();
        expect(ta.value).toBe('he X llo');
        const resp = messages[messages.length - 1];
        expect(resp.ok).toBe(true);
        channel.destroy();
    });

    it('forwards events to the extension', () => {
        const {  channel, messages , arm } = makeChannel([CAP_CONFIG_GET]);
        arm();
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

        const {  channel, messages , arm } = makeChannel([CAP_CREATE_TASK_PASTE]);
        arm(); // onGrant registers the paste listener on the textarea

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

        const {  channel, messages , arm } = makeChannel([CAP_CREATE_TASK_PASTE]);
        arm();
        channel.destroy(); // removes the listener via eventCleanups

        const ev = new Event('paste', { bubbles: true, cancelable: true }) as ClipboardEvent;
        Object.defineProperty(ev, 'clipboardData', { value: { getData: () => 'x' } });
        ta.dispatchEvent(ev);

        expect(messages.filter((m) => m.type === 'event').length).toBe(0);
    });

    it('destroy() removes the listener and the iframe, and stops responding', async () => {
        const {  channel, contentWindow, messages, iframe , arm } = makeChannel([CAP_CONFIG_GET]);
        arm();
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
        const {  channel, contentWindow , arm } = makeChannel([CAP_UI_STATUS], {}, anchor);
        arm();
        send(channel, contentWindow, { type: 'call', id: 'u1', cap: CAP_UI_STATUS, args: { state: 'loading', message: 'Importing…' } });
        await flush();
        const status = anchor.querySelector('[data-ext-status]') as HTMLElement;
        expect(status).toBeTruthy();
        expect(status.style.display).toBe('flex');
        expect(status.textContent).toContain('Importing…');
        channel.destroy();
    });

    it('renders an error and replaces the loading content', async () => {
        const {  channel, contentWindow , arm } = makeChannel([CAP_UI_STATUS], {}, anchor);
        arm();
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
        const {  channel, contentWindow , arm } = makeChannel([CAP_UI_STATUS], {}, anchor);
        arm();
        send(channel, contentWindow, { type: 'call', id: 'u1', cap: CAP_UI_STATUS, args: { state: 'loading', message: 'Importing…' } });
        await flush();
        send(channel, contentWindow, { type: 'call', id: 'u2', cap: CAP_UI_STATUS, args: { state: 'hidden' } });
        await flush();
        const status = anchor.querySelector('[data-ext-status]') as HTMLElement;
        expect(status.style.display).toBe('none');
        channel.destroy();
    });

    it('sizes the iframe via ui.setSize', async () => {
        const {  channel, contentWindow, iframe , arm } = makeChannel([CAP_UI_SET_SIZE], {}, anchor);
        arm();
        expect(iframe.style.height).not.toBe('120px');
        send(channel, contentWindow, { type: 'call', id: 'u3', cap: CAP_UI_SET_SIZE, args: { height: 120 } });
        await flush();
        expect(iframe.style.height).toBe('120px');
        channel.destroy();
    });

    it('does not render status when there is no anchor', async () => {
        const {  channel, contentWindow, messages , arm } = makeChannel([CAP_UI_STATUS], {});
        arm();
        send(channel, contentWindow, { type: 'call', id: 'u4', cap: CAP_UI_STATUS, args: { state: 'loading', message: 'x' } });
        await flush();
        // Should still respond ok, but no DOM was created.
        const resp = messages[messages.length - 1];
        expect(resp.ok).toBe(true);
        expect(document.querySelector('[data-ext-status]')).toBeNull();
        channel.destroy();
    });

    it('rejects an unknown ui.status state', async () => {
        const {  channel, contentWindow, messages , arm } = makeChannel([CAP_UI_STATUS], {});
        arm();
        send(channel, contentWindow, { type: 'call', id: 'u5', cap: CAP_UI_STATUS, args: { state: 'banana' } });
        await flush();
        const resp = messages[messages.length - 1];
        expect(resp.ok).toBe(false);
        expect(resp.error).toContain('unknown ui.status state');
        channel.destroy();
    });
});

describe('ExtensionHostChannel ui.modal', () => {
    let anchor: HTMLDivElement;

    beforeEach(() => {
        document.body.innerHTML = '';
        anchor = document.createElement('div');
        document.body.appendChild(anchor);
    });

    function dismissModal() {
        const btn = document.querySelector('[data-ext-modal] button') as HTMLButtonElement | null;
        expect(btn).toBeTruthy();
        btn!.click();
    }

    it('renders an app-level dialog with title, message, and attribution', async () => {
        const {  channel, contentWindow, messages , arm } = makeChannel([CAP_UI_MODAL], {});
        arm();
        send(channel, contentWindow, {
            type: 'call', id: 'm1', cap: CAP_UI_MODAL,
            args: { title: 'Preview stopped', message: 'The page navigated away.', confirmLabel: 'Close' },
        });
        await flush();
        // App-level: rendered into document.body, NOT the injection anchor.
        const overlay = document.querySelector('[data-ext-modal]');
        expect(overlay).toBeTruthy();
        expect(anchor.querySelector('[data-ext-modal]')).toBeNull();
        const dialog = overlay!.querySelector('[role="dialog"]');
        expect(dialog).toBeTruthy();
        expect(dialog!.getAttribute('aria-modal')).toBe('true');
        expect(overlay!.textContent).toContain('Preview stopped');
        expect(overlay!.textContent).toContain('The page navigated away.');
        expect(overlay!.textContent).toContain('Shown by Test');
        const btn = overlay!.querySelector('button') as HTMLButtonElement;
        expect(btn.textContent).toBe('Close');
        // No response yet — the call resolves when the user dismisses.
        expect(messages.some((m) => m.type === 'response' && m.id === 'm1')).toBe(false);
        dismissModal();
        await flush();
        expect(document.querySelector('[data-ext-modal]')).toBeNull();
        const resp = messages.find((m) => m.type === 'response' && m.id === 'm1');
        expect(resp && resp.ok).toBe(true);
        channel.destroy();
    });

    it('closes on Escape (topmost only when stacked)', async () => {
        const {  channel, contentWindow , arm } = makeChannel([CAP_UI_MODAL], {});
        arm();
        send(channel, contentWindow, { type: 'call', id: 'm2', cap: CAP_UI_MODAL, args: { message: 'first' } });
        await flush();
        send(channel, contentWindow, { type: 'call', id: 'm3', cap: CAP_UI_MODAL, args: { message: 'second' } });
        await flush();
        expect(document.querySelectorAll('[data-ext-modal]')).toHaveLength(2);
        document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
        await flush();
        // Only the topmost closed; the bottom one stays open.
        expect(document.querySelectorAll('[data-ext-modal]')).toHaveLength(1);
        expect(document.querySelector('[data-ext-modal]')!.textContent).toContain('first');
        document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
        await flush();
        expect(document.querySelectorAll('[data-ext-modal]')).toHaveLength(0);
        channel.destroy();
    });

    it('closes on backdrop click, not on dialog click', async () => {
        const {  channel, contentWindow , arm } = makeChannel([CAP_UI_MODAL], {});
        arm();
        send(channel, contentWindow, { type: 'call', id: 'm4', cap: CAP_UI_MODAL, args: { message: 'x' } });
        await flush();
        const overlay = document.querySelector('[data-ext-modal]') as HTMLElement;
        overlay.querySelector('[role="dialog"]')!.dispatchEvent(new MouseEvent('click', { bubbles: true }));
        await flush();
        expect(document.querySelector('[data-ext-modal]')).toBeTruthy();
        overlay.dispatchEvent(new MouseEvent('click'));
        await flush();
        expect(document.querySelector('[data-ext-modal]')).toBeNull();
        channel.destroy();
    });

    it('rejects a missing message arg', async () => {
        const {  channel, contentWindow, messages , arm } = makeChannel([CAP_UI_MODAL], {});
        arm();
        send(channel, contentWindow, { type: 'call', id: 'm5', cap: CAP_UI_MODAL, args: { title: 'no message' } });
        await flush();
        const resp = messages[messages.length - 1];
        expect(resp.ok).toBe(false);
        expect(resp.error).toContain("missing 'message'");
        expect(document.querySelector('[data-ext-modal]')).toBeNull();
        channel.destroy();
    });

    it('is not implicitly granted — an ungranted call renders nothing', async () => {
        const {  channel, contentWindow, messages , arm } = makeChannel([], {});
        arm();
        send(channel, contentWindow, { type: 'call', id: 'm6', cap: CAP_UI_MODAL, args: { message: 'x' } });
        await flush();
        const resp = messages[messages.length - 1];
        expect(resp.ok).toBe(false);
        expect(resp.error).toContain('capability not granted: ui.modal');
        expect(document.querySelector('[data-ext-modal]')).toBeNull();
        channel.destroy();
    });
});

describe('ExtensionHostChannel handshake (document identity)', () => {
    beforeEach(() => {
        document.body.innerHTML = '';
    });

    it('refuses capability calls and withholds init before the handshake', async () => {
        // The frame's document has not proven it arrived via the mounted URL,
        // so it must get neither the config/grants payload nor capability work.
        const { channel, contentWindow, messages } = makeChannel([CAP_CONFIG_GET]);
        channel.notifyFrameLoad(); // asks for a hello; nothing answered
        expect(initOf(messages)).toBeUndefined();

        send(channel, contentWindow, { type: 'call', id: 'h1', cap: CAP_CONFIG_GET, args: {} });
        await flush();
        // Silent: no dispatch, and no reply either — an unproven document learns
        // nothing about the channel.
        expect(messages.some((m) => m.type === 'response')).toBe(false);
        expect(initOf(messages)).toBeUndefined();
        channel.destroy();
    });

    it('keeps the channel disarmed on a wrong or missing token', async () => {
        const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
        const { channel, contentWindow, messages } = makeChannel([CAP_CONFIG_GET]);
        channel.notifyFrameLoad();
        send(channel, contentWindow, { type: 'exthello', token: 'guessed' } as any);
        send(channel, contentWindow, { type: 'exthello' } as any);
        await flush();
        expect(initOf(messages)).toBeUndefined();

        send(channel, contentWindow, { type: 'call', id: 'h2', cap: CAP_CONFIG_GET, args: {} });
        await flush();
        expect(messages.some((m) => m.type === 'response')).toBe(false);
        expect(warn).toHaveBeenCalled();
        warn.mockRestore();
        channel.destroy();
    });

    it('posts init exactly once per arm', () => {
        const { channel, contentWindow, messages } = makeChannel([CAP_CONFIG_GET]);
        channel.notifyFrameLoad();
        send(channel, contentWindow, { type: 'exthello', token: TEST_TOKEN } as any);
        // A repeat hello (e.g. a second exthello-request) must not re-send init.
        send(channel, contentWindow, { type: 'exthello', token: TEST_TOKEN } as any);
        expect(messages.filter((m) => m.type === 'init')).toHaveLength(1);
        channel.destroy();
    });

    it('withholds pushed events while disarmed and delivers them once armed', () => {
        const { channel, contentWindow, messages } = makeChannel([CAP_CONFIG_GET]);
        channel.notifyFrameLoad();
        channel.forward('editor.contentChange', { content: 'SECRET FILE TEXT' });
        expect(messages.some((m) => m.type === 'event')).toBe(false);

        send(channel, contentWindow, { type: 'exthello', token: TEST_TOKEN } as any);
        channel.forward('editor.contentChange', { content: 'SECRET FILE TEXT' });
        const ev = messages.find((m) => m.type === 'event');
        expect(ev && ev.data.content).toBe('SECRET FILE TEXT');
        channel.destroy();
    });

    it('warns and disarms on a frame reload, then re-arms on a fresh hello', async () => {
        const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
        const { channel, contentWindow, messages, iframe } = makeChannel([CAP_CONFIG_GET]);
        channel.notifyFrameLoad();
        send(channel, contentWindow, { type: 'exthello', token: TEST_TOKEN } as any);
        expect(initOf(messages)).toBeTruthy();

        // The frame navigated: warn-only watchdog (no teardown) + disarm.
        channel.notifyFrameLoad();
        expect(warn).toHaveBeenCalled();
        expect(iframe.remove).not.toHaveBeenCalled();

        const eventsBefore = messages.filter((m) => m.type === 'event').length;
        channel.forward('editor.contentChange', { content: 'SECRET FILE TEXT' });
        expect(messages.filter((m) => m.type === 'event').length).toBe(eventsBefore);

        send(channel, contentWindow, { type: 'call', id: 'h3', cap: CAP_CONFIG_GET, args: {} });
        await flush();
        expect(messages.some((m) => m.type === 'response' && m.id === 'h3')).toBe(false);

        // A genuine reload re-handshakes and keeps working.
        send(channel, contentWindow, { type: 'exthello', token: TEST_TOKEN } as any);
        expect(messages.filter((m) => m.type === 'init')).toHaveLength(2);
        send(channel, contentWindow, { type: 'call', id: 'h4', cap: CAP_CONFIG_GET, args: {} });
        await flush();
        const ok = messages.find((m) => m.type === 'response' && m.id === 'h4');
        expect(ok.ok).toBe(true);
        warn.mockRestore();
        channel.destroy();
    });

    it('runs event-capability cleanup on disarm so host listeners stop pushing', () => {
        const ta = document.createElement('textarea');
        ta.id = 'task-description';
        document.body.appendChild(ta);

        const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
        const { channel, contentWindow, messages } = makeChannel([CAP_CREATE_TASK_PASTE]);
        channel.notifyFrameLoad();
        send(channel, contentWindow, { type: 'exthello', token: TEST_TOKEN } as any);

        const fire = () => {
            const ev = new Event('paste', { bubbles: true, cancelable: true }) as ClipboardEvent;
            Object.defineProperty(ev, 'clipboardData', { value: { getData: () => 'https://github.com/a/b/issues/1' } });
            ta.dispatchEvent(ev);
        };

        fire();
        expect(messages.filter((m) => m.type === 'event')).toHaveLength(1);

        channel.notifyFrameLoad(); // reload -> disarm, listeners removed
        fire();
        expect(messages.filter((m) => m.type === 'event')).toHaveLength(1);

        send(channel, contentWindow, { type: 'exthello', token: TEST_TOKEN } as any);
        fire(); // re-armed: the listener is back
        expect(messages.filter((m) => m.type === 'event')).toHaveLength(2);
        warn.mockRestore();
        channel.destroy();
    });
});

describe('ExtensionHostChannel capability budget', () => {
    beforeEach(() => {
        document.body.innerHTML = '';
    });

    it('throttles capability calls past the per-channel budget', async () => {
        // Capability handlers reach the network and the user's editor, so a
        // runaway extension loop must not be able to spam them.
        const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
        const { channel, contentWindow, messages, arm } = makeChannel([CAP_CONFIG_GET]);
        arm();
        for (let i = 0; i < 65; i++) {
            send(channel, contentWindow, { type: 'call', id: 'r' + i, cap: CAP_CONFIG_GET, args: {} });
        }
        await flush();

        const ok = messages.filter((m) => m.type === 'response' && m.ok === true);
        const throttled = messages.filter((m) => m.type === 'response' && m.error === 'rate limit exceeded');
        expect(ok).toHaveLength(60);
        expect(throttled).toHaveLength(5);
        // One warning, not one per rejected call.
        expect(warn.mock.calls.filter((c) => String(c[0]).includes('throttled'))).toHaveLength(1);
        warn.mockRestore();
        channel.destroy();
    });
});
