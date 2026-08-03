/**
 * Handlers for the `createTask` area capabilities.
 *
 * These are the ONLY way a sandboxed extension can affect the createTask
 * modal. Each handler performs a single, narrowly-scoped operation on the
 * parent's real DOM. Note: values inserted into the textarea use `.value`
 * (plain text), never `innerHTML`, so a granted extension cannot smuggle
 * HTML/script through this surface.
 */
import type { CapabilityDef, HostContext } from '../types';

export const CAP_CREATE_TASK_INSERT_TEXT = 'createTask.description.insertText';
export const CAP_CREATE_TASK_PASTE = 'createTask.events.paste';

const DESCRIPTION_ID = 'task-description';

function descriptionTextarea(ctx: HostContext): HTMLTextAreaElement | null {
    // Prefer the element inside the anchor; fall back to the known modal id.
    const anchored = ctx.anchor?.querySelector<HTMLTextAreaElement>(`#${DESCRIPTION_ID}`);
    if (anchored) return anchored;
    return document.getElementById(DESCRIPTION_ID) as HTMLTextAreaElement | null;
}

/** Insert plain text into the task description at the cursor and notify the app. */
function insertDescriptionText(ctx: HostContext, args: { text: string }): void {
    const ta = descriptionTextarea(ctx);
    if (!ta) throw new Error('task description textarea not found');
    const start = ta.selectionStart ?? ta.value.length;
    const end = ta.selectionEnd ?? ta.value.length;
    const before = ta.value.substring(0, start);
    const after = ta.value.substring(end);
    // Plain text assignment only — never innerHTML.
    ta.value = before + args.text + after;
    // Move cursor to end of inserted text.
    const pos = start + args.text.length;
    try { ta.setSelectionRange(pos, pos); } catch {}
    // Dispatch input so the framework picks up the change.
    ta.dispatchEvent(new Event('input', { bubbles: true }));
}

/**
 * Forward paste events (clipboard text only) to subscribed extensions.
 *
 * `preventDefault()` stops the browser from inserting the raw pasted text.
 * This lets a subscribed extension fully own the insertion so it can replace
 * the pasted text with transformed content (e.g. a GitHub URL becomes the
 * issue body). The extension must re-insert the original text for pastes it
 * does not handle, otherwise the paste would be dropped.
 */
function onPasteGrant(ctx: HostContext): () => void {
    const ta = descriptionTextarea(ctx);
    if (!ta) return () => {};
    const listener = (e: ClipboardEvent) => {
        const text = e.clipboardData?.getData('text') || '';
        e.preventDefault();
        ctx.sendEvent(CAP_CREATE_TASK_PASTE, { text });
    };
    ta.addEventListener('paste', listener);
    return () => ta.removeEventListener('paste', listener);
}

export const createTaskCapabilities: Record<string, CapabilityDef> = {
    [CAP_CREATE_TASK_INSERT_TEXT]: {
        kind: 'call',
        argsSchema: { text: 'string' },
        handler: insertDescriptionText,
    },
    [CAP_CREATE_TASK_PASTE]: {
        kind: 'event',
        onGrant: onPasteGrant,
    },
};
