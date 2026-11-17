/** The composer's draft — its text and its image tray: the store holds what the surface holds, empty
 * included. Each surface keeps this tab's belief about the store, and a read answers only a surface that
 * still says what the belief said when it began. The two `MayPaint` rules judge that differently, because a
 * tray write lands where the change was made and a keystroke's write is debounced. */

import type { DraftAttachment } from '$lib/storage';

/** What the drafts store holds for a task, as this tab last wrote it; `null` until the opening read answers. */
export type StoredDraft = string | null;

/** Whether the box's content has to reach the store. */
export function draftNeedsSave(stored: StoredDraft, box: string): boolean {
    // Writing an empty box before the read answers would delete the draft that read is bringing back.
    return stored === null ? box !== '' : box !== stored;
}

/** Whether the answer to a draft read may fill the box: only a box still holding what was believed when the
 * read began, and nothing after a write of ours. The write a keystroke earns is debounced, so during that
 * wait the box is the only record of a change that has not reached the store. */
export function draftMayPaint(box: string, believed: StoredDraft, wroteSinceRead: boolean): boolean {
    if (wroteSinceRead) return false;
    return believed === null ? box === '' : box === believed;
}

/** As `StoredDraft`, for the image tray. Only this tab's writes and an adopted read move it: taking a late
 * read's answer as the belief would undo the deletion that read raced. */
export type StoredDraftAttachments = DraftAttachment[] | null;

/** The tray's images plain of any `$state` proxy and of the caller's array. */
export function rememberedAttachments(tray: DraftAttachment[]): DraftAttachment[] {
    return tray.map(a => ({ mimeType: a.mimeType, data: a.data }));
}

/** Whether the tray's images have to reach the store. */
export function draftAttachmentsNeedSave(stored: StoredDraftAttachments, tray: DraftAttachment[]): boolean {
    // An empty tray says nothing about the store while the opening read is on its way; deleting then
    // removes the draft that read is bringing back.
    if (stored === null) return tray.length > 0;
    return !sameAttachments(stored, tray);
}

/** Whether the answer to an attachments read may fill the tray: only an untouched one, and nothing after a
 * write of ours. */
export function draftAttachmentsMayPaint(tray: DraftAttachment[], wroteSinceRead: boolean): boolean {
    return !wroteSinceRead && tray.length === 0;
}

function sameAttachments(a: DraftAttachment[], b: DraftAttachment[]): boolean {
    return a.length === b.length
        && a.every((att, i) => att.mimeType === b[i].mimeType && att.data === b[i].data);
}
