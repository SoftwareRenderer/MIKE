import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { ExtensionHostChannel } from '../channel';
import {
    CAP_EDITOR_ADD_FOOTER_BUTTON,
    CAP_EDITOR_REMOVE_FOOTER_BUTTON,
    CAP_EDITOR_FOOTER_CLICK,
    CAP_EDITOR_GET_CONTENT,
    CAP_EDITOR_SET_CONTENT,
    CAP_EDITOR_SET_OVERLAY,
    CAP_EDITOR_CONTENT_CHANGE,
    CAP_EDITOR_SAVE,
} from '../capabilities';
import { computeGrants, SUPPORTED_CAPABILITIES } from '../capabilities';
import { cleanupFooterButtons, CONTENT_SET_USER_EVENT, isExtensionContentSet, MAX_FOOTER_BUTTONS_PER_INSTANCE } from '../handlers/editor';
import type { HostRequest } from '../types';

function makeFakeIframe() {
    const messages: any[] = [];
    const contentWindow = { postMessage: (msg: any) => messages.push(msg) };
    const iframe: any = {
        contentWindow,
        style: {} as CSSStyleDeclaration,
        remove: vi.fn(),
    };
    return { iframe, contentWindow, messages };
}

function buildEditorDom(): { root: HTMLElement; anchor: HTMLElement; footerLeft: HTMLElement } {
    document.body.innerHTML = '';
    const root = document.createElement('div');
    root.className = 'file-editor';

    const content = document.createElement('div');
    content.className = 'editor-content';
    const wrapper = document.createElement('div');
    wrapper.className = 'editor-wrapper';
    content.appendChild(wrapper);
    const anchor = document.createElement('div');
    anchor.className = 'ext-anchor';
    content.appendChild(anchor);
    root.appendChild(content);

    const footer = document.createElement('div');
    footer.className = 'editor-footer';
    const footerLeft = document.createElement('div');
    footerLeft.className = 'footer-left';
    const wrap = document.createElement('button');
    wrap.title = 'Toggle word wrap';
    wrap.textContent = 'Wrap';
    footerLeft.appendChild(wrap);
    footer.appendChild(footerLeft);
    root.appendChild(footer);

    document.body.appendChild(root);
    return { root, anchor, footerLeft };
}

const TEST_TOKEN = 'test-channel-token-0123456789abcdef';

function makeChannel(granted: string[], anchor: HTMLElement) {
    const { iframe, contentWindow, messages } = makeFakeIframe();
    const channel = new ExtensionHostChannel({
        extensionId: 'ext-md',
        extensionName: 'MarkdownPreview',
        point: 'fileEditor' as any,
        granted,
        config: {},
        context: { component: 'FileEditor', filePath: '/repo/README.md' },
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
    return { channel, contentWindow, messages, iframe };
}

function send(_channel: ExtensionHostChannel, contentWindow: any, msg: HostRequest) {
    window.dispatchEvent(new MessageEvent('message', { data: msg, source: contentWindow as any, origin: 'null' }));
}

const flush = () => new Promise((r) => setTimeout(r, 0));

describe('editor capability catalog', () => {
    it('registers all editor.* capabilities', () => {
        // NOTE: editor.footerButtonClick is intentionally NOT a capability —
        // clicking a button an extension created is intrinsic to
        // editor.addFooterButton, not a grantable permission.
        for (const cap of [
            'editor.addFooterButton',
            'editor.removeFooterButton',
            'editor.getContent',
            'editor.setOverlay',
            'editor.contentChange',
        ]) {
            expect(SUPPORTED_CAPABILITIES[cap]).toBeTruthy();
        }
        expect(SUPPORTED_CAPABILITIES['editor.footerButtonClick']).toBeUndefined();
    });

    it('grants declared editor capabilities through computeGrants', () => {
        const granted = computeGrants([
            'editor.addFooterButton',
            'editor.getContent',
            'editor.setOverlay',
            'editor.contentChange',
        ]);
        expect(granted).toEqual([
            'editor.addFooterButton',
            'editor.getContent',
            'editor.setOverlay',
            'editor.contentChange',
        ]);
        expect(computeGrants(['editor.addFooterButton', 'files.read'])).toEqual(['editor.addFooterButton']);
    });
});

describe('editor.addFooterButton', () => {
    let anchor: HTMLElement, footerLeft: HTMLElement;
    beforeEach(() => { ({ anchor, footerLeft } = buildEditorDom()); });
    afterEach(() => cleanupFooterButtons('ext-md'));

    it('adds a button to the footer right after Wrap', async () => {
        const { channel, contentWindow, messages } = makeChannel([CAP_EDITOR_ADD_FOOTER_BUTTON], anchor);
        send(channel, contentWindow, { type: 'call', id: 'b1', cap: CAP_EDITOR_ADD_FOOTER_BUTTON, args: { id: 'md-preview', label: 'Preview' } });
        await flush();

        const btn = footerLeft.querySelector('button[data-ext-footer-btn="md-preview"]') as HTMLButtonElement;
        expect(btn).toBeTruthy();
        expect(btn.textContent).toBe('Preview');
        // Marked as a toggle (like Wrap) so it gets the enabled outline style.
        expect(btn.classList.contains('footer-toggle')).toBe(true);
        // Placed immediately after the Wrap button.
        const wrap = footerLeft.querySelector('button[title="Toggle word wrap"]');
        expect(btn.previousElementSibling).toBe(wrap);

        const resp = messages[messages.length - 1];
        expect(resp.ok).toBe(true);
        channel.destroy();
    });

    it('forwards a created button click as an editor.footerButtonClick event', async () => {
        // Only addFooterButton is granted — footerButtonClick is NOT a
        // capability. The click on a button the extension itself created is
        // intrinsic to addFooterButton, so the event is delivered regardless.
        const { channel, contentWindow, messages } = makeChannel([CAP_EDITOR_ADD_FOOTER_BUTTON], anchor);
        send(channel, contentWindow, { type: 'call', id: 'b1', cap: CAP_EDITOR_ADD_FOOTER_BUTTON, args: { id: 'md-preview', label: 'Preview' } });
        await flush();

        const btn = footerLeft.querySelector('button[data-ext-footer-btn="md-preview"]') as HTMLButtonElement;
        btn.click();

        const ev = messages.filter((m) => m.type === 'event').pop();
        expect(ev).toBeTruthy();
        expect(ev.cap).toBe(CAP_EDITOR_FOOTER_CLICK);
        expect(ev.data.id).toBe('md-preview');
        channel.destroy();
    });

    it('updates active state without resetting the label', async () => {
        const { channel, contentWindow } = makeChannel([CAP_EDITOR_ADD_FOOTER_BUTTON], anchor);
        send(channel, contentWindow, { type: 'call', id: 'b1', cap: CAP_EDITOR_ADD_FOOTER_BUTTON, args: { id: 'md-preview', label: 'Preview' } });
        await flush();
        // Pure active-state update (no label).
        send(channel, contentWindow, { type: 'call', id: 'b2', cap: CAP_EDITOR_ADD_FOOTER_BUTTON, args: { id: 'md-preview', active: true } });
        await flush();

        const btn = footerLeft.querySelector('button[data-ext-footer-btn="md-preview"]') as HTMLButtonElement;
        expect(btn.classList.contains('active')).toBe(true);
        expect(btn.textContent).toBe('Preview');
        channel.destroy();
    });

    it('editor.footerRemoveButton removes the button', async () => {
        const { channel, contentWindow } = makeChannel([CAP_EDITOR_ADD_FOOTER_BUTTON, CAP_EDITOR_REMOVE_FOOTER_BUTTON], anchor);
        send(channel, contentWindow, { type: 'call', id: 'b1', cap: CAP_EDITOR_ADD_FOOTER_BUTTON, args: { id: 'md-preview', label: 'Preview' } });
        await flush();
        expect(footerLeft.querySelector('button[data-ext-footer-btn="md-preview"]')).toBeTruthy();

        send(channel, contentWindow, { type: 'call', id: 'r1', cap: CAP_EDITOR_REMOVE_FOOTER_BUTTON, args: { id: 'md-preview' } });
        await flush();
        expect(footerLeft.querySelector('button[data-ext-footer-btn="md-preview"]')).toBeNull();
        channel.destroy();
    });

    it('channel.destroy cleans up footer buttons', async () => {
        const { channel, contentWindow } = makeChannel([CAP_EDITOR_ADD_FOOTER_BUTTON], anchor);
        send(channel, contentWindow, { type: 'call', id: 'b1', cap: CAP_EDITOR_ADD_FOOTER_BUTTON, args: { id: 'md-preview', label: 'Preview' } });
        await flush();
        expect(footerLeft.querySelector('button[data-ext-footer-btn="md-preview"]')).toBeTruthy();

        channel.destroy();
        expect(footerLeft.querySelector('button[data-ext-footer-btn="md-preview"]')).toBeNull();
    });

    it('rejects a button call when the footer is missing', async () => {
        const { channel, contentWindow, messages } = makeChannel([CAP_EDITOR_ADD_FOOTER_BUTTON], anchor);
        anchor.closest = () => null as any; // simulate no .file-editor ancestor
        send(channel, contentWindow, { type: 'call', id: 'b1', cap: CAP_EDITOR_ADD_FOOTER_BUTTON, args: { id: 'md-preview', label: 'Preview' } });
        await flush();
        const resp = messages[messages.length - 1];
        expect(resp.ok).toBe(false);
        expect(resp.error).toContain('editor footer not found');
        channel.destroy();
    });
});

describe('editor.getContent and editor.setOverlay', () => {
    let root: HTMLElement, anchor: HTMLElement;
    beforeEach(() => { ({ root, anchor } = buildEditorDom()); });
    afterEach(() => cleanupFooterButtons('ext-md'));

    it('editor.getContent returns the CodeMirror document text', async () => {
        const wrapper = root.querySelector('.editor-wrapper') as HTMLElement;
        const cm = document.createElement('div');
        cm.className = 'cm-editor';
        (cm as any).__view = { state: { doc: { toString: () => '# Title\n\nBody' } } };
        wrapper.appendChild(cm);

        const { channel, contentWindow, messages } = makeChannel([CAP_EDITOR_GET_CONTENT], anchor);
        send(channel, contentWindow, { type: 'call', id: 'g1', cap: CAP_EDITOR_GET_CONTENT, args: {} });
        await flush();
        const resp = messages[messages.length - 1];
        expect(resp.ok).toBe(true);
        expect(resp.data.content).toBe('# Title\n\nBody');
        channel.destroy();
    });

    it('editor.setOverlay(true) hides the editor and sizes the iframe', async () => {
        const wrapper = root.querySelector('.editor-wrapper') as HTMLElement;
        const { channel, contentWindow, iframe } = makeChannel([CAP_EDITOR_SET_OVERLAY], anchor);
        send(channel, contentWindow, { type: 'call', id: 'o1', cap: CAP_EDITOR_SET_OVERLAY, args: { visible: true } });
        await flush();
        expect(wrapper.style.display).toBe('none');
        // iframe fills its anchor (100%), which fills .editor-content — so the
        // overlay tracks editor/panel resizes automatically.
        expect(iframe.style.height).toBe('100%');
        channel.destroy();
    });

    it('editor.setOverlay(false) restores the editor', async () => {
        const wrapper = root.querySelector('.editor-wrapper') as HTMLElement;
        wrapper.style.display = 'none';
        const { channel, contentWindow } = makeChannel([CAP_EDITOR_SET_OVERLAY], anchor);
        send(channel, contentWindow, { type: 'call', id: 'o1', cap: CAP_EDITOR_SET_OVERLAY, args: { visible: false } });
        await flush();
        expect(wrapper.style.display).not.toBe('none');
        channel.destroy();
    });

    it('editor.contentChange is a grantable event capability', () => {
        expect(SUPPORTED_CAPABILITIES[CAP_EDITOR_CONTENT_CHANGE].kind).toBe('event');
        expect(computeGrants([CAP_EDITOR_CONTENT_CHANGE])).toContain(CAP_EDITOR_CONTENT_CHANGE);
    });
});

describe('editor.setContent and isExtensionContentSet', () => {
    let root: HTMLElement, anchor: HTMLElement;
    beforeEach(() => { ({ root, anchor } = buildEditorDom()); });
    afterEach(() => cleanupFooterButtons('ext-md'));

    it('dispatches the replacement with the CONTENT_SET_USER_EVENT marker', async () => {
        const wrapper = root.querySelector('.editor-wrapper') as HTMLElement;
        const cm = document.createElement('div');
        cm.className = 'cm-editor';
        const dispatch = vi.fn();
        (cm as any).__view = {
            state: { doc: { toString: () => 'old text' } },
            dispatch,
        };
        wrapper.appendChild(cm);

        const { channel, contentWindow } = makeChannel([CAP_EDITOR_SET_CONTENT], anchor);
        send(channel, contentWindow, { type: 'call', id: 'sc1', cap: CAP_EDITOR_SET_CONTENT, args: { content: 'new text' } });
        await flush();

        expect(dispatch).toHaveBeenCalledTimes(1);
        const spec = dispatch.mock.calls[0][0];
        expect(spec.userEvent).toBe(CONTENT_SET_USER_EVENT);
        expect(spec.changes).toEqual({ from: 0, to: 8, insert: 'new text' });
        channel.destroy();
    });

    it('is a no-op when the content is unchanged', async () => {
        const wrapper = root.querySelector('.editor-wrapper') as HTMLElement;
        const cm = document.createElement('div');
        cm.className = 'cm-editor';
        const dispatch = vi.fn();
        (cm as any).__view = {
            state: { doc: { toString: () => 'same' } },
            dispatch,
        };
        wrapper.appendChild(cm);

        const { channel, contentWindow } = makeChannel([CAP_EDITOR_SET_CONTENT], anchor);
        send(channel, contentWindow, { type: 'call', id: 'sc2', cap: CAP_EDITOR_SET_CONTENT, args: { content: 'same' } });
        await flush();
        expect(dispatch).not.toHaveBeenCalled();
        channel.destroy();
    });

    it('recognizes an update made solely of extension setContent transactions', () => {
        // A single push (the normal case from the handler).
        expect(isExtensionContentSet([CONTENT_SET_USER_EVENT])).toBe(true);
        // A batched multi-transaction dispatch where every transaction is a push.
        expect(isExtensionContentSet([CONTENT_SET_USER_EVENT, CONTENT_SET_USER_EVENT])).toBe(true);
    });

    it('does NOT recognize user input, external edits, or mixed updates', () => {
        expect(isExtensionContentSet(['input.delete'])).toBe(false);
        expect(isExtensionContentSet([undefined])).toBe(false);
        expect(isExtensionContentSet(['input.delete', CONTENT_SET_USER_EVENT])).toBe(false);
        expect(isExtensionContentSet([])).toBe(false);
    });
});

describe('editor.save', () => {
    let anchor: HTMLElement;
    beforeEach(() => { ({ anchor } = buildEditorDom()); });
    afterEach(() => cleanupFooterButtons('ext-md'));

    it('is a grantable call capability', () => {
        expect(SUPPORTED_CAPABILITIES[CAP_EDITOR_SAVE].kind).toBe('call');
        expect(computeGrants([CAP_EDITOR_SAVE])).toContain(CAP_EDITOR_SAVE);
    });

    it('dispatches a scoped editor-save-request on its own editor root', async () => {
        // Bubbling (not window-dispatched) so a listener anywhere still sees
        // it, but detail.scope names the owning instance.
        const { channel, contentWindow, messages } = makeChannel([CAP_EDITOR_SAVE], anchor);
        const listener = vi.fn();
        window.addEventListener('editor-save-request', listener);
        send(channel, contentWindow, { type: 'call', id: 's1', cap: CAP_EDITOR_SAVE, args: {} });
        await flush();

        expect(listener).toHaveBeenCalledTimes(1);
        const ev = listener.mock.calls[0][0] as CustomEvent;
        const root = anchor.closest('.file-editor') as HTMLElement;
        expect(ev.detail?.scope).toBe(root);
        const resp = messages[messages.length - 1];
        expect(resp.ok).toBe(true);
        window.removeEventListener('editor-save-request', listener);
        channel.destroy();
    });

    it('names only the editor instance the extension is mounted in', async () => {
        // Two editors open at once: a save requested through the extension in
        // editor A must reach A alone — never "whichever file is active".
        const { root: rootA, anchor: anchorA } = buildEditorDom();
        const anchorAEl = anchorA;
        document.body.appendChild(rootA.cloneNode(true)); // a second .file-editor
        const rootB = document.querySelectorAll('.file-editor')[1] as HTMLElement;
        const anchorB = rootB.querySelector('.ext-anchor') as HTMLElement;

        const chanA = makeChannel([CAP_EDITOR_SAVE], anchorAEl);
        const chanB = makeChannel([CAP_EDITOR_SAVE], anchorB);

        const scoped: EventTarget[] = [];
        const capture = (e: Event) => { scoped.push((e as CustomEvent).detail.scope); };
        window.addEventListener('editor-save-request', capture);

        send(chanA.channel, chanA.contentWindow, { type: 'call', id: 'sA', cap: CAP_EDITOR_SAVE, args: {} });
        await flush();

        expect(scoped).toEqual([rootA]);
        window.removeEventListener('editor-save-request', capture);
        chanA.channel.destroy();
        chanB.channel.destroy();
    });

    it('rejects when the editor root is missing', async () => {
        const { channel, contentWindow, messages } = makeChannel([CAP_EDITOR_SAVE], anchor);
        anchor.closest = () => null as any; // simulate no .file-editor ancestor
        send(channel, contentWindow, { type: 'call', id: 's1', cap: CAP_EDITOR_SAVE, args: {} });
        await flush();
        const resp = messages[messages.length - 1];
        expect(resp.ok).toBe(false);
        expect(resp.error).toContain('editor not found');
        channel.destroy();
    });
});

describe('editor.addFooterButton budget', () => {
    let anchor: HTMLElement, footerLeft: HTMLElement;
    beforeEach(() => { ({ anchor, footerLeft } = buildEditorDom()); });
    afterEach(() => cleanupFooterButtons('ext-md'));

    it('caps how many buttons one extension may own in one editor', async () => {
        const { channel, contentWindow, messages } = makeChannel([CAP_EDITOR_ADD_FOOTER_BUTTON], anchor);
        for (let i = 0; i < MAX_FOOTER_BUTTONS_PER_INSTANCE; i++) {
            send(channel, contentWindow, { type: 'call', id: 'fb' + i, cap: CAP_EDITOR_ADD_FOOTER_BUTTON, args: { id: 'b' + i } });
            await flush();
        }
        expect(footerLeft.querySelectorAll(`button[data-ext-footer-ext="ext-md"]`)).toHaveLength(MAX_FOOTER_BUTTONS_PER_INSTANCE);

        // One more (new id) is refused instead of flooding the host footer.
        send(channel, contentWindow, { type: 'call', id: 'fbX', cap: CAP_EDITOR_ADD_FOOTER_BUTTON, args: { id: 'over' } });
        await flush();
        const over = messages.find((m) => m.id === 'fbX');
        expect(over.ok).toBe(false);
        expect(over.error).toContain('too many editor footer buttons');
        expect(footerLeft.querySelector('button[data-ext-footer-btn="over"]')).toBeNull();

        // Re-declaring an id it already owns stays an update, not a new slot.
        send(channel, contentWindow, { type: 'call', id: 'fbU', cap: CAP_EDITOR_ADD_FOOTER_BUTTON, args: { id: 'b0', label: 'Renamed' } });
        await flush();
        const upd = messages.find((m) => m.id === 'fbU');
        expect(upd.ok).toBe(true);
        expect(footerLeft.querySelector('button[data-ext-footer-btn="b0"]')!.textContent).toBe('Renamed');
        channel.destroy();
    });
});
