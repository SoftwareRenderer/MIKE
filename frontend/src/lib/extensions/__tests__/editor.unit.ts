import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { ExtensionHostChannel } from '../channel';
import {
    CAP_EDITOR_ADD_FOOTER_BUTTON,
    CAP_EDITOR_REMOVE_FOOTER_BUTTON,
    CAP_EDITOR_FOOTER_CLICK,
    CAP_EDITOR_GET_CONTENT,
    CAP_EDITOR_SET_OVERLAY,
    CAP_EDITOR_CONTENT_CHANGE,
    CAP_EDITOR_SAVE,
} from '../capabilities';
import { computeGrants, SUPPORTED_CAPABILITIES } from '../capabilities';
import { cleanupFooterButtons } from '../handlers/editor';
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
    });
    return { channel, contentWindow, messages, iframe };
}

function send(channel: ExtensionHostChannel, contentWindow: any, msg: HostRequest) {
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
    let root: HTMLElement, anchor: HTMLElement, footerLeft: HTMLElement;
    beforeEach(() => { ({ root, anchor, footerLeft } = buildEditorDom()); });
    afterEach(() => cleanupFooterButtons('ext-md'));

    it('adds a button to the footer right after Wrap', async () => {
        const { channel, contentWindow, messages } = makeChannel([CAP_EDITOR_ADD_FOOTER_BUTTON], anchor);
        channel.start();
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
        channel.start();
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
        channel.start();
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
        channel.start();
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
        channel.start();
        send(channel, contentWindow, { type: 'call', id: 'b1', cap: CAP_EDITOR_ADD_FOOTER_BUTTON, args: { id: 'md-preview', label: 'Preview' } });
        await flush();
        expect(footerLeft.querySelector('button[data-ext-footer-btn="md-preview"]')).toBeTruthy();

        channel.destroy();
        expect(footerLeft.querySelector('button[data-ext-footer-btn="md-preview"]')).toBeNull();
    });

    it('rejects a button call when the footer is missing', async () => {
        const { channel, contentWindow, messages } = makeChannel([CAP_EDITOR_ADD_FOOTER_BUTTON], anchor);
        channel.start();
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
    let root: HTMLElement, anchor: HTMLElement, footerLeft: HTMLElement;
    beforeEach(() => { ({ root, anchor, footerLeft } = buildEditorDom()); });
    afterEach(() => cleanupFooterButtons('ext-md'));

    it('editor.getContent returns the CodeMirror document text', async () => {
        const wrapper = root.querySelector('.editor-wrapper') as HTMLElement;
        const cm = document.createElement('div');
        cm.className = 'cm-editor';
        (cm as any).__view = { state: { doc: { toString: () => '# Title\n\nBody' } } };
        wrapper.appendChild(cm);

        const { channel, contentWindow, messages } = makeChannel([CAP_EDITOR_GET_CONTENT], anchor);
        channel.start();
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
        channel.start();
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
        channel.start();
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

describe('editor.save', () => {
    let root: HTMLElement, anchor: HTMLElement, footerLeft: HTMLElement;
    beforeEach(() => { ({ root, anchor, footerLeft } = buildEditorDom()); });
    afterEach(() => cleanupFooterButtons('ext-md'));

    it('is a grantable call capability', () => {
        expect(SUPPORTED_CAPABILITIES[CAP_EDITOR_SAVE].kind).toBe('call');
        expect(computeGrants([CAP_EDITOR_SAVE])).toContain(CAP_EDITOR_SAVE);
    });

    it('dispatches an editor-save-request event on window', async () => {
        const { channel, contentWindow, messages } = makeChannel([CAP_EDITOR_SAVE], anchor);
        const listener = vi.fn();
        window.addEventListener('editor-save-request', listener);
        channel.start();
        send(channel, contentWindow, { type: 'call', id: 's1', cap: CAP_EDITOR_SAVE, args: {} });
        await flush();

        expect(listener).toHaveBeenCalledTimes(1);
        const resp = messages[messages.length - 1];
        expect(resp.ok).toBe(true);
        window.removeEventListener('editor-save-request', listener);
        channel.destroy();
    });

    it('rejects when the editor root is missing', async () => {
        const { channel, contentWindow, messages } = makeChannel([CAP_EDITOR_SAVE], anchor);
        channel.start();
        anchor.closest = () => null as any; // simulate no .file-editor ancestor
        send(channel, contentWindow, { type: 'call', id: 's1', cap: CAP_EDITOR_SAVE, args: {} });
        await flush();
        const resp = messages[messages.length - 1];
        expect(resp.ok).toBe(false);
        expect(resp.error).toContain('editor not found');
        channel.destroy();
    });
});
