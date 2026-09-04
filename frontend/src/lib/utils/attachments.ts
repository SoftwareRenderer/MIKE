/**
 * Picking images for a message — one rule, four entry points.
 *
 * The chat form, both edit forms and the create-task modal all take a file input, keep
 * the images, read them to a data URL and store `{ mimeType, data }` (see
 * `DraftAttachment` in storage). Those copies drifted: only the chat form noticed an
 * image that was too big, so a pasted 5 MB screenshot in the other three simply vanished.
 * The limits and the "these didn't make it" sentence live here so every entry point
 * answers the same way.
 */
import type { DraftAttachment } from '$lib/storage';
import { t } from '$lib/i18n/index.svelte';

/** Largest image we accept, in bytes. */
export const MAX_IMAGE_SIZE = 4 * 1024 * 1024;

/** Images per message / per task. */
export const MAX_IMAGES = 4;

export interface PickedImages {
    /** Read and ready to attach, in the order they were picked. */
    accepted: DraftAttachment[];
    /** Images over {@link MAX_IMAGE_SIZE} — they look like a failed attach, so say so. */
    oversize: File[];
    /** Valid images that had no room left under the count cap. */
    overflow: File[];
}

/** What a picker's selection splits into, before anything is read. */
export interface ClassifiedImages {
    /** Images we may attach, truncated to `remaining`. */
    readable: File[];
    oversize: File[];
    overflow: File[];
}

/** Whether a picked file is an image we would try to read at all. */
export function isImageFile(file: File): boolean {
    return file.type.startsWith('image/');
}

/**
 * Sort a file picker's selection into what may be attached, what is too big, and what
 * had no room. Lives apart from the reading step because one caller (the chat form)
 * keeps the `File` itself alongside the decoded image, for its previews.
 */
export function classifyImages(files: FileList | File[], remaining: number): ClassifiedImages {
    const images = Array.from(files).filter(isImageFile);
    const oversize = images.filter(f => f.size > MAX_IMAGE_SIZE);
    const readable = images.filter(f => f.size <= MAX_IMAGE_SIZE);
    const room = Math.max(0, remaining);
    return { readable: readable.slice(0, room), oversize, overflow: readable.slice(room) };
}

/** Read one file as its data URL (`data:image/png;base64,…`). */
export function readImageDataUrl(file: File): Promise<string> {
    return new Promise((resolve, reject) => {
        const reader = new FileReader();
        reader.onload = () => resolve(reader.result as string);
        reader.onerror = () => reject(reader.error ?? new Error(t('attachments.couldNotRead', { name: file.name })));
        reader.readAsDataURL(file);
    });
}

/**
 * Turn a file picker's selection into attachments: images only, at most `remaining` of
 * them, none over {@link MAX_IMAGE_SIZE}. Whatever did not make it comes back for the
 * caller to report — never silently dropped.
 */
export async function readImageAttachments(files: FileList | File[], remaining: number): Promise<PickedImages> {
    const { readable, oversize, overflow } = classifyImages(files, remaining);

    const accepted: DraftAttachment[] = [];
    for (const file of readable) {
        try {
            accepted.push({ mimeType: file.type, data: await readImageDataUrl(file) });
        } catch (err) {
            // A file the browser won't hand over (moved, permission revoked) is reported
            // rather than left as a hole in the message.
            console.error('Failed to read attachment:', err);
            oversize.push(file);
        }
    }

    return { accepted, oversize, overflow };
}

/** The sentence for what was left out, or null when everything was accepted. */
export function describeRejectedImages(rejected: { oversize: File[]; overflow: File[] }): string | null {
    if (rejected.oversize.length > 0) {
        const n = rejected.oversize.length;
        const limit = Math.round(MAX_IMAGE_SIZE / (1024 * 1024));
        return t('attachments.tooLarge', { count: n, limit });
    }
    if (rejected.overflow.length > 0) return t('attachments.onlyN', { max: MAX_IMAGES });
    return null;
}
