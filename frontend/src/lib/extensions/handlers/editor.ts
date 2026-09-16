/**
 * Handlers for the `editor` capabilities — the only way a sandboxed extension can touch
 * the code editor: addFooterButton, getContent, setContent, setOverlay, contentChange.
 *
 * `editor.footerButtonClick` and `editor.removeFooterButton` are not separately grantable:
 * clicking or removing a button the extension created is intrinsic to
 * `addFooterButton` (the event carries only the id the extension supplied), so both are
 * granted with it. Nothing here interprets what the extension renders.
 */
import type { CapabilityDef, HostContext } from '../types';

export const CAP_EDITOR_ADD_FOOTER_BUTTON = 'editor.addFooterButton';
export const CAP_EDITOR_REMOVE_FOOTER_BUTTON = 'editor.removeFooterButton';
export const CAP_EDITOR_FOOTER_CLICK = 'editor.footerButtonClick';
export const CAP_EDITOR_GET_CONTENT = 'editor.getContent';
export const CAP_EDITOR_SET_CONTENT = 'editor.setContent';
export const CAP_EDITOR_SET_OVERLAY = 'editor.setOverlay';
export const CAP_EDITOR_CONTENT_CHANGE = 'editor.contentChange';
const CAP_EDITOR_JUMP_TO_LINE = 'editor.jumpToLine';
export const CAP_EDITOR_SAVE = 'editor.save';

/**
 * `userEvent` marker for the transaction dispatched by `editor.setContent`
 * (below). The FileEditor's updateListener uses it to recognize an
 * extension's own push and NOT echo it back as editor.contentChange — see
 * {@link isExtensionContentSet}.
 */
export const CONTENT_SET_USER_EVENT = 'setContent';

/**
 * True when EVERY transaction in a CodeMirror update came from an extension's
 * editor.setContent push. Such updates must not be forwarded as editor.contentChange: the
 * extension already knows the text it sent, and a delayed echo of a superseded push makes a
 * WYSIWYG extension rewrite its document from stale text and yank the caret.
 *
 * Takes the structural `userEvent` shape so it is unit-testable without a real view.
 */
export function isExtensionContentSet(userEvents: readonly (string | undefined)[]): boolean {
    return userEvents.length > 0 && userEvents.every(e => e === CONTENT_SET_USER_EVENT);
}

// Buttons created by extensions, keyed `${extensionId}:${buttonId}`. Because a
// point (e.g. `fileEditor`) can be mounted in many instances at once (one open
// file each), the value tracks one cleanup per anchor instance — so each open
// file keeps its own independent Preview button. Removed on demand and cleaned
// up when the owning instance's channel is torn down.
const footerButtons = new Map<string, Map<HTMLElement | null, () => void>>();

/**
 * How many footer buttons one extension may own in one editor instance. The
 * footer is host UI shared with LSP status and the file's own controls, so a
 * granted `editor.addFooterButton` must not be able to flood it.
 */
export const MAX_FOOTER_BUTTONS_PER_INSTANCE = 8;

/** Buttons this extension already owns in `anchor` (by button id). */
function ownedButtonIds(extensionId: string, anchor: HTMLElement | null): string[] {
    const prefix = `${extensionId}:`;
    const ids: string[] = [];
    for (const [key, anchors] of footerButtons) {
        if (!key.startsWith(prefix) || !anchors.has(anchor)) continue;
        ids.push(key.slice(prefix.length));
    }
    return ids;
}

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

    // Budget: re-declaring an id it already owns is an update, a NEW id is
    // capped.
    const owned = ownedButtonIds(ctx.extensionId, anchor);
    if (!owned.includes(args.id) && owned.length >= MAX_FOOTER_BUTTONS_PER_INSTANCE) {
        anchors.delete(anchor);
        if (anchors.size === 0) footerButtons.delete(key);
        throw new Error(
            `too many editor footer buttons (max ${MAX_FOOTER_BUTTONS_PER_INSTANCE} per extension)`
        );
    }

    // Replace an existing button with the same id *in this same instance
    // (anchor)*. If no label is supplied (a pure active-state update),
    // preserve the previous label. A button in a different instance is left
    // untouched — each open file owns its own button.
    const existingCleanup = anchors.get(anchor);
    let prevLabel = '';
    if (existingCleanup) {
        // Compared through `dataset` because `args.id` came from the frame: in a selector string
        // a `"` in it would end the attribute value and drive the selector.
        const prev = Array.from(left.querySelectorAll<HTMLButtonElement>('button[data-ext-footer-btn]'))
            .find(b => b.dataset.extFooterExt === ctx.extensionId && b.dataset.extFooterBtn === args.id);
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

/** Removing an absent button is a no-op. */
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
 * Replace the entire editor document text, for WYSIWYG-style extensions pushing overlay
 * edits back into the code editor. It flows through the normal `updateListener` (dirty
 * tracking, drafts, history all apply) but carries the {@link CONTENT_SET_USER_EVENT}
 * marker so it is not echoed back as `contentChange` (see {@link isExtensionContentSet}).
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
        userEvent: CONTENT_SET_USER_EVENT,
    });
}

/**
 * Toggle a full-bleed overlay over the editor content area: true hides the code editor and
 * sizes the extension iframe to fill it, false restores both. The anchor is generic; the
 * extension decides what it renders.
 */
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
 * Request the host save the file this extension instance is mounted in.
 *
 * The event is dispatched on the extension's OWN `.file-editor` root (bubbling,
 * so a window-level listener still sees it) carrying `detail.scope`. The tab bar
 * dispatches the same event on `window` unscoped, where "the active editor" is
 * the right target; with several editors open an extension must not be able to
 * save whichever file happens to be active, so only the instance in `scope`
 * responds — see FileEditor's `handleSaveRequest`.
 */
function saveFile(ctx: HostContext): void {
    const root = editorRoot(ctx);
    if (!root) throw new Error('editor not found');
    root.dispatchEvent(new CustomEvent('editor-save-request', {
        bubbles: true,
        detail: { scope: root },
    }));
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
