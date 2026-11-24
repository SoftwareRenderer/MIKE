/**
 * One classifier for what a paste, drop or pick carried. The wire to a model emits only `text`
 * and `image_url` parts and nothing else, so only an image has a delivery path and every other
 * file has to be one the agent opens — which is what decides the three buckets.
 */
import type { DraftAttachment } from '$lib/storage';
import { t } from '$lib/i18n';
import { DEFAULT_FILE_SIZE_LIMIT_BYTES } from './file-drop';

export const MAX_IMAGES = 4;

/**
 * The pixel ceiling on the image a provider is shown: above it the file is resampled here, at intake, so the
 * tray, the draft, the row and the request all hold the smaller picture.
 *
 * @knipignore The geometry table measures against this ceiling.
 */
export const MAX_IMAGE_PIXELS = 16_000_000;

/**
 * A paste has no `accept`, so this is the only thing between a log file and the prompt.
 *
 * @knipignore Both readers are measured at this byte cap, including what they report as dropped.
 */
export const MAX_INLINE_TEXT = 64 * 1024;

/** Why an image could not stay an image. */
type ImageFallback = 'oversize' | 'unresizable';

export interface PickedImages {
    accepted: DraftAttachment[];
    /** Images that cannot become an `image_url` part, each with its reason: they go to the task's files. */
    asFiles: { file: File; reason: ImageFallback }[];
    /** Images that fit every cap but the count. */
    overflow: File[];
}

/**
 * A decoded picture: its size, and the resample. `encode` answers nothing when the browser cannot write that
 * format, which is how a GIF — something no canvas can write — leaves the tray.
 */
interface DecodedImage {
    width: number;
    height: number;
    encode(mimeType: string, width: number, height: number): Promise<Blob | null>;
    close(): void;
}

/** How bytes become a picture. Injected, so the rules below are measured without a canvas. */
export type DecodeImage = (file: File) => Promise<DecodedImage | null>;

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
function isTextFile(file: File): boolean {
    if (file.type === 'image/svg+xml') return true;
    if (file.type.startsWith('text/')) return true;
    return TEXT_APPLICATION_TYPES.has(file.type);
}

function isAttachableImage(file: File): boolean {
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
 * `DataTransfer` because a paste carries `clipboardData` and a drop carries `dataTransfer`.
 */
type ImageTransfer = { files: FileList | File[] } & Partial<Pick<DataTransfer, 'getData'>>;

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
function transferCarriesText(transfer: ImageTransfer | null | undefined, files: File[] = []): boolean {
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

/**
 * takeTransfer returns the files a paste or a drop carried, or null when they carried none, so the
 * caller leaves the browser's own text alone. It cancels the event here, synchronously, because
 * after one `await` the browser has already pasted.
 */
export function takeTransfer(event: ImageTransferEvent): TransferBuckets | null {
    const transfer = event.dataTransfer ?? event.clipboardData ?? null;
    const all = transfer ? Array.from(transfer.files ?? []) : [];
    if (all.length === 0) return null;
    const buckets = sortTransferFiles(all);
    if (!transferCarriesText(transfer, all)) event.preventDefault();
    return buckets;
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
 * The largest integer pair on the source's ratio that fits `MAX_IMAGE_PIXELS`, or null when it already fits,
 * which keeps the image's own bytes instead of re-encoding it.
 *
 * @knipignore The geometry table measures this.
 */
export function scaledImageDimensions(width: number, height: number): { width: number; height: number } | null {
    if (width <= 0 || height <= 0 || width * height <= MAX_IMAGE_PIXELS) return null;
    const scale = Math.sqrt(MAX_IMAGE_PIXELS / (width * height));
    const w = Math.max(1, Math.floor(width * scale));
    const h = Math.max(1, Math.floor(height * scale));
    // A side that floors to zero is one pixel, and the other is then bounded by the ceiling itself, so the
    // product still fits a strip of any ratio.
    return { width: h === 1 ? Math.min(w, MAX_IMAGE_PIXELS) : w, height: w === 1 ? Math.min(h, MAX_IMAGE_PIXELS) : h };
}

/** The bytes of `file` as bare base64, the form the tray, the draft and the row hold; null when they cannot be read. */
async function readImageBase64(file: File): Promise<string | null> {
    try {
        const url = await new Promise<string>((resolve, reject) => {
            const reader = new FileReader();
            reader.onload = () => resolve(String(reader.result));
            reader.onerror = () => reject(reader.error);
            reader.readAsDataURL(file);
        });
        return url.slice(url.indexOf(',') + 1);
    } catch (err) {
        console.error('Failed to read attachment:', err);
        return null;
    }
}

async function blobToBase64(blob: Blob): Promise<string> {
    const bytes = new Uint8Array(await blob.arrayBuffer());
    // btoa over one string built by repeated concatenation would hold a copy per chunk of the image.
    const CHUNK = 0x8000;
    let binary = '';
    for (let i = 0; i < bytes.length; i += CHUNK) {
        binary += String.fromCharCode(...bytes.subarray(i, i + CHUNK));
    }
    return btoa(binary);
}

/**
 * The one place a resample is owed: an image inside both caps keeps the bytes it arrived as, and a bigger one
 * is drawn down to the dimensions its ratio allows. An image that cannot be pictured, written or read
 * answers null, and the caller attaches it as a file.
 */
async function toAttachment(file: File, decode: DecodeImage): Promise<DraftAttachment | null> {
    let image: DecodedImage | null;
    try {
        image = await decode(file);
    } catch (err) {
        console.error('Failed to decode attachment:', err);
        return null;
    }
    if (!image) return null;
    try {
        const target = scaledImageDimensions(image.width, image.height);
        if (!target) {
            const data = await readImageBase64(file);
            return data ? { mimeType: file.type, data } : null;
        }
        const blob = await image.encode(file.type, target.width, target.height);
        if (!blob) return null;
        return { mimeType: file.type, data: await blobToBase64(blob) };
    } finally {
        image.close();
    }
}

/**
 * The browser's own decoder. A format it will not write answers null rather than the PNG a canvas falls back
 * to, so the provider is shown the format that was attached.
 */
async function decodeImage(file: File): Promise<DecodedImage | null> {
    const bitmap = await createImageBitmap(file);
    return {
        width: bitmap.width,
        height: bitmap.height,
        async encode(mimeType, width, height) {
            const canvas = document.createElement('canvas');
            canvas.width = width;
            canvas.height = height;
            const ctx = canvas.getContext('2d');
            if (!ctx) return null;
            ctx.drawImage(bitmap, 0, 0, width, height);
            const blob = await new Promise<Blob | null>((resolve) => canvas.toBlob(resolve, mimeType));
            return blob && blob.type === mimeType ? blob : null;
        },
        close: () => bitmap.close?.(),
    };
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
export async function readImageAttachments(
    files: FileList | File[],
    remaining: number,
    decode: DecodeImage = decodeImage,
    maxBytes: number = DEFAULT_FILE_SIZE_LIMIT_BYTES,
): Promise<PickedImages> {
    let room = Math.max(0, remaining);
    const accepted: DraftAttachment[] = [];
    const asFiles: PickedImages['asFiles'] = [];
    const overflow: File[] = [];

    // Whatever is not an attachable image is not this function's business: a picker can be talked past its
    // `accept`, and those files belong to `sortTransferFiles`.
    for (const file of Array.from(files).filter(isAttachableImage)) {
        if (file.size > maxBytes) asFiles.push({ file, reason: 'oversize' });
        else if (room === 0) overflow.push(file);
        else {
            const attachment = await toAttachment(file, decode);
            if (attachment) {
                accepted.push(attachment);
                room -= 1;
            } else asFiles.push({ file, reason: 'unresizable' });
        }
    }

    return { accepted, asFiles, overflow };
}

/**
 * The sentence for a pick that left no image in the tray. `uploaded` is false where there is no task to write
 * into: the file path reports its own refusal there, so claiming an upload would be wrong.
 */
export function describeRejectedImages(
    rejected: PickedImages,
    uploaded = true,
    maxBytes: number = DEFAULT_FILE_SIZE_LIMIT_BYTES,
): string | null {
    const sentences: string[] = [];
    if (uploaded) {
        const limit = Math.round(maxBytes / (1024 * 1024));
        const oversize = rejected.asFiles.filter(e => e.reason === 'oversize').length;
        const unresizable = rejected.asFiles.filter(e => e.reason === 'unresizable').length;
        if (oversize > 0) sentences.push(t('attachments.sentAsFilesOversize', { count: oversize, limit }));
        if (unresizable > 0) sentences.push(t('attachments.sentAsFilesUnresizable', { count: unresizable }));
    }
    if (rejected.overflow.length > 0) sentences.push(t('attachments.onlyN', { max: MAX_IMAGES }));
    return sentences.length > 0 ? sentences.join('; ') : null;
}
