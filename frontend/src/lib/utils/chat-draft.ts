/** The chat input's draft: the store holds exactly what the box holds, empty included. A save that
 * skips an emptied box leaves text nobody deleted; a read that paints whatever it finds overwrites
 * text typed since it began. */

/** What the drafts store holds for a task, as this tab last wrote it; `null` until the opening read answers. */
export type StoredDraft = string | null;

/** Whether the box's content has to reach the store. */
export function draftNeedsSave(stored: StoredDraft, box: string): boolean {
    // Writing an empty box before the read answers would delete the draft that read is bringing back.
    return stored === null ? box !== '' : box !== stored;
}

/** Whether the answer to a draft read may fill the box: only one that is still empty and untouched
 * since the read began. */
export function draftMayPaint(box: string, wroteSinceRead: boolean): boolean {
    return !wroteSinceRead && box === '';
}
