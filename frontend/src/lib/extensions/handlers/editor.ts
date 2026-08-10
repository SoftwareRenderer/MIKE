/**
 * Handlers for the `editor` area capabilities — generic, content-agnostic
 * building blocks for file-editor UI extensions.
 *
 * These are the ONLY way a sandboxed extension can touch the code editor:
 *   - add a button to the editor footer (`editor.addFooterButton`)
 *   - read the current editor content (`editor.getContent`)
 *   - replace the editor content (`editor.setContent`) — e.g. to push edits made
 *     in a WYSIWYG overlay back into the code editor
 *   - hide/show the code editor to reveal an extension overlay (`editor.setOverlay`)
 *   - receive editor content updates (`editor.contentChange`)
 *
 * `editor.footerButtonClick` and `editor.removeFooterButton` are NOT separately
 * grantable. Clicking a button the extension itself created is intrinsic to
 * `editor.addFooterButton` — the click event only ever carries the button's
 * `id`, which the extension supplied. Likewise, removing a button it created is
 * scoped to its own buttons only. Both are granted implicitly alongside
 * `editor.addFooterButton` and need no manifest declaration.
 *
 * Nothing here understands what the extension does with these primitives
 * (e.g. markdown preview). The core stays feature-agnostic — the extension
 * decides what a footer button or an overlay means.
 */
import type { CapabilityDef, HostContext } from '../types';

export const CAP_EDITOR_ADD_FOOTER_BUTTON = 'editor.addFooterButton';
export const CAP_EDITOR_REMOVE_FOOTER_BUTTON = 'editor.removeFooterButton';
export const CAP_EDITOR_FOOTER_CLICK = 'editor.footerButtonClick';
export const CAP_EDITOR_GET_CONTENT = 'editor.getContent';
export const CAP_EDITOR_SET_CONTENT = 'editor.setContent';
export const CAP_EDITOR_SET_OVERLAY = 'editor.setOverlay';
export const CAP_EDITOR_CONTENT_CHANGE = 'editor.contentChange';
export const CAP_EDITOR_JUMP_TO_LINE = 'editor.jumpToLine';
export const CAP_EDITOR_SAVE = 'editor.save';

// Buttons created by extensions, keyed `${extensionId}:${buttonId}`. Because a
// point (e.g. `fileEditor`) can be mounted in many instances at once (one open
// file each), the value tracks one cleanup per anchor instance — so each open
// file keeps its own independent Preview button. Removed on demand and cleaned
// up when the owning instance's channel is torn down.
const footerButtons = new Map<string, Map<HTMLElement | null, () => void>>();

/** The `.file-editor` root containing the anchor for this extension. */
function editorRoot(ctx: HostContext): HTMLElement | null {
    if (!ctx.anchor) return null;
    return ctx.anchor.closest('.file-editor') as HTMLElement | null;
}

/** The footer's left cluster (where LSP status, Ln/Col, and Wrap live). */
function footerLeft(ctx: HostContext): HTMLElement | null {
    const root = editorRoot(ctx);
    if (!root) return null;
    return root.querySelector('.editor-footer .footer-left') as HTMLElement | null;
}

/**
 * Add (or update) a button in the editor footer, right after "Wrap".
 * Idempotent: calling again with the same `id` replaces the button and its
 * click wiring. `label` and `active` are optional — omit them to only update
 * the active state without touching the label.
 */
function addFooterButton(ctx: HostContext, args: { id: string; label?: string; active?: boolean }): void {
    const left = footerLeft(ctx);
    if (!left) throw new Error('editor footer not found');
    const key = `${ctx.extensionId}:${args.id}`;
    const anchor = ctx.anchor ?? null;

    let anchors = footerButtons.get(key);
    if (!anchors) {
        anchors = new Map();
        footerButtons.set(key, anchors);
    }

    // Replace an existing button with the same id *in this same instance
    // (anchor)*. If no label is supplied (a pure active-state update),
    // preserve the previous label. A button in a different instance is left
    // untouched — each open file owns its own button.
    const existingCleanup = anchors.get(anchor);
    let prevLabel = '';
    if (existingCleanup) {
        const prev = left.querySelector<HTMLButtonElement>(
            `button[data-ext-footer-ext="${ctx.extensionId}"][data-ext-footer-btn="${args.id}"]`
        );
        if (prev) prevLabel = prev.textContent || '';
        existingCleanup();
        anchors.delete(anchor);
    }

    const label = args.label !== undefined && args.label !== null ? String(args.label) : (prevLabel || args.id);
    const btn = document.createElement('button');
    btn.type = 'button';
    // `ext-footer-btn` is styled globally (EditorFooter.svelte) to match the
    // editor footer buttons. The component's scoped `footer-btn` class cannot
    // apply to DOM injected outside the component, so we rely on the global
    // companion rule only.
    // `footer-toggle` marks the button as a toggle (like Wrap) so it picks up
    // the enabled outline style when `active` is set. Styling for both classes
    // lives in EditorFooter.svelte.
    btn.className = 'ext-footer-btn footer-toggle';
    btn.dataset.extFooterBtn = args.id;
    btn.dataset.extFooterExt = ctx.extensionId;
    btn.textContent = label;
    btn.title = label;
    if (args.active) btn.classList.add('active');

    btn.addEventListener('click', () => {
        ctx.sendEvent(CAP_EDITOR_FOOTER_CLICK, { id: args.id });
    });

    // Place right after the Wrap button so extension buttons appear next to it.
    const wrapBtn = left.querySelector('button[title="Toggle word wrap"]');
    if (wrapBtn && wrapBtn.nextSibling) {
        left.insertBefore(btn, wrapBtn.nextSibling);
    } else {
        left.appendChild(btn);
    }

    anchors.set(anchor, () => btn.remove());
}

/** Remove a previously added footer button (no-op if absent). */
function removeFooterButton(ctx: HostContext, args: { id: string }): void {
    const key = `${ctx.extensionId}:${args.id}`;
    const anchor = ctx.anchor ?? null;
    const anchors = footerButtons.get(key);
    if (!anchors) return;
    const cleanup = anchors.get(anchor);
    if (cleanup) {
        cleanup();
        anchors.delete(anchor);
    }
    if (anchors.size === 0) footerButtons.delete(key);
}

/** Return the current editor document text. */
function getContent(ctx: HostContext): { content: string } {
    const root = editorRoot(ctx);
    if (!root) throw new Error('editor not found');
    const cm = root.querySelector('.cm-editor') as any;
    if (!cm || !cm.__view) throw new Error('editor not ready');
    return { content: cm.__view.state.doc.toString() };
}

/**
 * Toggle a full-bleed overlay over the editor content area.
 *
 * - `visible: true`  — hide the code editor and size the extension iframe to
 *   fill the editor content area, so the extension can render its own view.
 * - `visible: false` — restore the code editor and collapse the iframe.
 *
 * The overlay anchor is generic; the extension decides what it renders.
 */
/**
 * Replace the entire editor document text. Used by WYSIWYG-style extensions
 * (e.g. a rich-text markdown preview) to push edits made in an overlay back
 * into the code editor. The change flows through the editor's normal
 * `updateListener`, so the editor's `contentChange` event fires back to the
 * extension with the new text — extensions must guard against that echo.
 */
function setContent(ctx: HostContext, args: { content: string }): void {
    const root = editorRoot(ctx);
    if (!root) throw new Error('editor not found');
    const cm = root.querySelector('.cm-editor') as any;
    const view = cm && cm.__view;
    if (!view) throw new Error('editor not ready');
    const current = view.state.doc.toString();
    if (current === args.content) return; // no-op
    view.dispatch({
        changes: { from: 0, to: current.length, insert: args.content },
        userEvent: 'setContent',
    });
}

function setOverlay(ctx: HostContext, args: { visible: boolean }): void {
    const root = editorRoot(ctx);
    if (!root || !ctx.anchor) throw new Error('editor overlay target not found');
    const wrapper = root.querySelector('.editor-wrapper') as HTMLElement | null;
    const contentEl = root.querySelector('.editor-content') as HTMLElement | null;
    if (!wrapper || !contentEl) throw new Error('editor overlay target not found');

    if (args.visible) {
        wrapper.style.display = 'none';
        // Fill the editor content area (above the footer). The iframe is sized
        // to its anchor, which already fills .editor-content, so the overlay
        // resizes automatically with the editor/panel (no stale pixel height).
        ctx.ui.setFill();
    } else {
        wrapper.style.display = '';
        ctx.ui.setSize(0);
    }
}

/**
 * Request the host save the current file.
 *
 * The FileEditor listens for the `editor-save-request` window event and
 * performs the save, guarded by its own active/unsaved/disabled checks.
 */
function saveFile(ctx: HostContext): void {
    const root = editorRoot(ctx);
    if (!root) throw new Error('editor not found');
    window.dispatchEvent(new CustomEvent('editor-save-request'));
}

/**
 * Remove all footer buttons registered by an extension. Called by the host
 * when an extension channel is destroyed (e.g. editor closes, file changes).
 */
export function cleanupFooterButtons(extensionId: string, anchor?: HTMLElement | null): void {
    const prefix = `${extensionId}:`;
    for (const [key, anchors] of footerButtons) {
        if (!key.startsWith(prefix)) continue;
        if (anchor) {
            const cleanup = anchors.get(anchor);
            if (cleanup) {
                cleanup();
                anchors.delete(anchor);
            }
            if (anchors.size === 0) footerButtons.delete(key);
        } else {
            for (const cleanup of anchors.values()) cleanup();
            footerButtons.delete(key);
        }
    }
}

export const editorCapabilities: Record<string, CapabilityDef> = {
    [CAP_EDITOR_ADD_FOOTER_BUTTON]: {
        kind: 'call',
        argsSchema: { id: 'string' },
        handler: addFooterButton,
    },
    [CAP_EDITOR_REMOVE_FOOTER_BUTTON]: {
        kind: 'call',
        argsSchema: { id: 'string' },
        handler: removeFooterButton,
    },
    [CAP_EDITOR_GET_CONTENT]: {
        kind: 'call',
        argsSchema: {},
        handler: getContent,
    },
    [CAP_EDITOR_SET_CONTENT]: {
        kind: 'call',
        argsSchema: { content: 'string' },
        handler: setContent,
    },
    [CAP_EDITOR_SET_OVERLAY]: {
        kind: 'call',
        argsSchema: { visible: 'boolean' },
        handler: setOverlay,
    },
    [CAP_EDITOR_CONTENT_CHANGE]: { kind: 'event' },
    [CAP_EDITOR_JUMP_TO_LINE]: { kind: 'event' },
    [CAP_EDITOR_SAVE]: {
        kind: 'call',
        argsSchema: {},
        handler: saveFile,
    },
};
