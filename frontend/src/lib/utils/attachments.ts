/**
 * One classifier for what a paste, drop or pick carried. The wire to a model emits only `text`
 * and `image_url` parts and nothing else, so only an image has a delivery path and every other
 * file has to be one the agent opens — which is what decides the three buckets.
 */
import type { DraftAttachment } from '$lib/storage';
import { t } from '$lib/i18n';

export const MAX_IMAGE_SIZE = 4 * 1024 * 1024;

export const MAX_IMAGES = 4;

/** A paste has no `accept`, so this is the only thing between a log file and the prompt. */
export const MAX_INLINE_TEXT = 64 * 1024;

export interface PickedImages {
    accepted: DraftAttachment[];
    oversize: File[];
    overflow: File[];
}

/** What a picker's selection splits into, before anything is read. */
export interface ClassifiedImages {
    readable: File[];
    oversize: File[];
    overflow: File[];
}

/**
 * The rasters a provider will be shown. `NewImagePart` passes the mime straight into
 * `image_url`, so anything outside this set is a request the provider rejects, taking the whole
 * message with it.
 */
const ATTACHABLE_IMAGE_TYPES = new Set(['image/png', 'image/jpeg', 'image/gif', 'image/webp']);

/**
 * The same set in the form a picker's `accept` takes. Only a surface that can carry *nothing else* uses
 * it: a message input classifies whatever arrives (image, inlined text, or a file written into the task's
 * environment), so restricting its picker would grey out entries the handler already knows what to do
 * with. A surface with no task environment — the create-task modal — has nothing to upload into, so its
 * picker offers images and stops there.
 */
export const ATTACHABLE_IMAGE_ACCEPT = [...ATTACHABLE_IMAGE_TYPES].join(',');

/** `application/*` types whose bytes are text. Anything else under `application/` is a file. */
const TEXT_APPLICATION_TYPES = new Set([
    'application/json',
    'application/xml',
    'application/javascript',
    'application/x-javascript',
    'application/typescript',
    'application/yaml',
    'application/x-yaml',
    'application/sql',
    'application/csv',
    'application/x-sh',
    'application/toml',
]);

/**
 * Whether the model can read these bytes as text — a different question from what the browser
 * calls the file: an SVG is `image/*` and markup to everything else.
 */
export function isTextFile(file: File): boolean {
    if (file.type === 'image/svg+xml') return true;
    if (file.type.startsWith('text/')) return true;
    return TEXT_APPLICATION_TYPES.has(file.type);
}

export function isAttachableImage(file: File): boolean {
    return ATTACHABLE_IMAGE_TYPES.has(file.type);
}

export interface TransferBuckets {
    images: File[];
    texts: File[];
    files: File[];
}

/** Text is asked first, because it claims what `image/*` also claims. */
export function sortTransferFiles(files: File[]): TransferBuckets {
    const buckets: TransferBuckets = { images: [], texts: [], files: [] };
    for (const file of files) {
        if (isTextFile(file)) buckets.texts.push(file);
        else if (isAttachableImage(file)) buckets.images.push(file);
        else buckets.files.push(file);
    }
    return buckets;
}

/**
 * The part of a `DataTransfer` these helpers read. Declared by shape rather than named
 * `DataTransfer` because that is what a test can hand without a browser.
 */
export type ImageTransfer = { files: FileList | File[] } & Partial<Pick<DataTransfer, 'getData'>>;

export interface ImageTransferEvent {
    preventDefault(): void;
    readonly clipboardData?: ImageTransfer | null;
    readonly dataTransfer?: ImageTransfer | null;
}

/**
 * Whether the transfer carries text the user would miss. A copied file often arrives with its own
 * name, path or `file://` URI in `text/plain`, and that is the file's label — inserting it would
 * put `screenshot.png` in front of the screenshot.
 */
export function transferCarriesText(transfer: ImageTransfer | null | undefined, files: File[] = []): boolean {
    if (!transfer || typeof transfer.getData !== 'function') return false;
    const text = (transfer.getData('text/plain') || transfer.getData('text') || '').trim();
    if (!text) return false;
    const names = new Set(files.flatMap(f => [f.name, f.webkitRelativePath].filter(Boolean)));
    const isFileLabel = (line: string) => {
        const trimmed = line.trim();
        if (!trimmed) return true;
        if (names.has(trimmed)) return true;
        const base = trimmed.replace(/^file:\/\//, '').split(/[\\/]/).filter(Boolean).pop();
        return base !== undefined && names.has(base);
    };
    return text.split('\n').some(line => !isFileLabel(line));
}

/** A gesture's files and the decision about the browser's own insertion. */
export interface TransferIntake extends TransferBuckets {
    /** True when the caller must cancel the browser's own insertion. */
    owns: boolean;
}

/**
 * Take what a paste or a drop carried, or null when it carried no file — the caller must then
 * leave the browser's own text insertion alone. `preventDefault` happens here, synchronously:
 * after one `await` the browser has already pasted.
 */
export function takeTransfer(event: ImageTransferEvent): TransferIntake | null {
    const transfer = event.dataTransfer ?? event.clipboardData ?? null;
    const all = transfer ? Array.from(transfer.files ?? []) : [];
    if (all.length === 0) return null;
    const buckets = sortTransferFiles(all);
    if (!transferCarriesText(transfer, all)) {
        event.preventDefault();
        return { ...buckets, owns: true };
    }
    return { ...buckets, owns: false };
}

/** The cap is applied to the blob before anything is read, so a big file never becomes memory. */
export function readTextFile(file: File): Promise<{ content: string; droppedBytes: number }> {
    return new Promise((resolve, reject) => {
        const droppedBytes = Math.max(0, file.size - MAX_INLINE_TEXT);
        const reader = new FileReader();
        reader.onload = () => resolve({ content: String(reader.result ?? ''), droppedBytes });
        reader.onerror = () => reject(reader.error ?? new Error(t('attachments.couldNotRead', { name: file.name })));
        reader.readAsText(file.slice(0, MAX_INLINE_TEXT), 'utf-8');
    });
}

/**
 * Whatever is not an attachable image is not this function's business: a picker can be talked
 * past its `accept`, and those files belong to `sortTransferFiles`.
 */
export function classifyImages(files: FileList | File[], remaining: number): ClassifiedImages {
    const images = Array.from(files).filter(isAttachableImage);
    const oversize = images.filter(f => f.size > MAX_IMAGE_SIZE);
    const readable = images.filter(f => f.size <= MAX_IMAGE_SIZE);
    const room = Math.max(0, remaining);
    return { readable: readable.slice(0, room), oversize, overflow: readable.slice(room) };
}

export function readImageDataUrl(file: File): Promise<string> {
    return new Promise((resolve, reject) => {
        const reader = new FileReader();
        reader.onload = () => resolve(reader.result as string);
        reader.onerror = () => reject(reader.error ?? new Error(t('attachments.couldNotRead', { name: file.name })));
        reader.readAsDataURL(file);
    });
}

const DATA_URL_SCHEME = 'data:';
const DATA_URL_BASE64 = ';base64,';

/** Rows written before `data` was bare base64 carry a full data URL, so both spellings are
 *  accepted here rather than at every `<img>`. */
export function imageSrc(att: { mimeType: string; data: string }): string {
    return att.data.startsWith(DATA_URL_SCHEME)
        ? att.data
        : `${DATA_URL_SCHEME}${att.mimeType}${DATA_URL_BASE64}${att.data}`;
}

/** `data` is the **bare** base64, not a data URL: what the `attachments` column and IndexedDB
 *  drafts hold, and what `NewImagePart` wants with no envelope on it. */
export async function readImageAttachments(files: FileList | File[], remaining: number): Promise<PickedImages> {
    const { readable, oversize, overflow } = classifyImages(files, remaining);

    const accepted: DraftAttachment[] = [];
    for (const file of readable) {
        try {
            const url = await readImageDataUrl(file);
            accepted.push({ mimeType: file.type, data: url.slice(url.indexOf(',') + 1) });
        } catch (err) {
            // Reported as a rejection rather than left as a hole in the message.
            console.error('Failed to read attachment:', err);
            oversize.push(file);
        }
    }

    return { accepted, oversize, overflow };
}

export function describeRejectedImages(rejected: { oversize: File[]; overflow: File[] }): string | null {
    if (rejected.oversize.length > 0) {
        const n = rejected.oversize.length;
        const limit = Math.round(MAX_IMAGE_SIZE / (1024 * 1024));
        return t('attachments.tooLarge', { count: n, limit });
    }
    if (rejected.overflow.length > 0) return t('attachments.onlyN', { max: MAX_IMAGES });
    return null;
}
