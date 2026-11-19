import { describe, it, expect } from 'vitest';
import {
    describeRejectedImages,
    imageSrc,
    readImageAttachments,
    readTextFile,
    scaledImageDimensions,
    sortTransferFiles,
    takeTransfer,
    MAX_IMAGES,
    MAX_IMAGE_PIXELS,
    MAX_IMAGE_SIZE,
    MAX_INLINE_TEXT,
    type DecodeImage,
} from '../attachments';

const KB = 1024;
const MB = 1024 * 1024;

function image(name = 'shot.png', bytes = 2 * KB, type = 'image/png'): File {
    return new File([new Uint8Array(bytes)], name, { type });
}

// A File whose size is over the cap without allocating 20 MB.
function oversized(name = 'huge.png'): File {
    const f = image(name);
    Object.defineProperty(f, 'size', { value: MAX_IMAGE_SIZE + 1 });
    return f;
}

/** A blob with the type a canvas gives it, so an unwritable format is visible. */
function encodedBlob(bytes: number[], type = 'image/png'): Blob {
    return new Blob([new Uint8Array(bytes)], { type });
}

/**
 * A decoder that pictures every file at these dimensions. `writes` is what a resample returns, and `asked`
 * records the dimensions it was asked for.
 */
function picture(width: number, height: number, writes: Blob | null = encodedBlob([1, 2, 3])) {
    const asked: { mime: string; width: number; height: number }[] = [];
    let closed = 0;
    const decode: DecodeImage = async () => ({
        width,
        height,
        encode: async (mime, w, h) => {
            asked.push({ mime, width: w, height: h });
            return writes;
        },
        close: () => {
            closed += 1;
        },
    });
    return { decode, asked, closedCount: () => closed };
}

/** A decoder for bytes no browser can picture. */
const undecodable: DecodeImage = async () => null;

describe('the caps', () => {
    it('lets an image be exactly as large as a file the same gesture uploads', () => {
        // backend/exec/task_files.go's MaxUploadBytes is the number this mirrors.
        expect(MAX_IMAGE_SIZE).toBe(20 * MB);
    });
});

describe('sortTransferFiles', () => {
    it('sorts one gesture into the three things the app can do', () => {
        const sorted = sortTransferFiles([
            image('shot.png'),
            new File(['x'], 'run.log', { type: 'text/plain' }),
            new File([''], 'bundle.tar.gz', { type: 'application/gzip' }),
        ]);
        expect(sorted.images.map(f => f.name)).toEqual(['shot.png']);
        expect(sorted.texts.map(f => f.name)).toEqual(['run.log']);
        expect(sorted.files.map(f => f.name)).toEqual(['bundle.tar.gz']);
    });

    it('asks text before image, so an SVG is markup and not an image_url the provider rejects', () => {
        expect(sortTransferFiles([new File(['<svg/>'], 'a.svg', { type: 'image/svg+xml' })]).texts).toHaveLength(1);
    });

    it('takes only the rasters a provider will be shown', () => {
        for (const type of ['image/png', 'image/jpeg', 'image/gif', 'image/webp']) {
            expect(sortTransferFiles([image('a', 2, type)]).images).toHaveLength(1);
        }
    });

    it('refuses what the browser calls an image but the provider rejects', () => {
        for (const file of [
            image('a.heic', 2, 'image/heic'),
            image('a.tif', 2, 'image/tiff'),
        ]) {
            expect(sortTransferFiles([file]).files.map(f => f.name)).toEqual([file.name]);
        }
    });

    it('reads anything whose bytes are text as a document', () => {
        for (const file of [
            new File([''], 'a.txt', { type: 'text/plain' }),
            new File([''], 'a.log', { type: 'text/x-log' }),
            new File([''], 'a.json', { type: 'application/json' }),
            new File([''], 'a.yaml', { type: 'application/yaml' }),
            new File([''], 'a.svg', { type: 'image/svg+xml' }),
        ]) {
            expect(sortTransferFiles([file]).texts.map(f => f.name)).toEqual([file.name]);
        }
    });

    it('leaves a real document and an unknown type as files', () => {
        for (const file of [
            new File([''], 'a.pdf', { type: 'application/pdf' }),
            new File([''], 'a.zip', { type: 'application/zip' }),
            new File([''], 'no-type'),
        ]) {
            expect(sortTransferFiles([file]).files.map(f => f.name)).toEqual([file.name]);
        }
    });
});

describe('describeRejectedImages', () => {
    const none = { accepted: [], asFiles: [], overflow: [] };

    it('says nothing when everything was accepted', () => {
        expect(describeRejectedImages(none)).toBeNull();
    });

    it('names the size limit for an image that went to the task as a file', () => {
        const asFiles = [{ file: oversized(), reason: 'oversize' as const }];
        expect(describeRejectedImages({ ...none, asFiles }))
            .toBe(`1 image over ${Math.round(MAX_IMAGE_SIZE / MB)} MB attached as a file`);
        expect(describeRejectedImages({
            ...none,
            asFiles: [...asFiles, { file: oversized('b.png'), reason: 'oversize' as const }],
        })).toContain('2 images over');
    });

    it('names the resample, not the size, for an image the browser could not draw smaller', () => {
        const asFiles = [{ file: image(), reason: 'unresizable' as const }];
        expect(describeRejectedImages({ ...none, asFiles })).toBe('1 image could not be scaled down, attached as a file');
    });

    it('says both when a pick carried both reasons', () => {
        const said = describeRejectedImages({
            ...none,
            asFiles: [{ file: oversized(), reason: 'oversize' }, { file: image('b.gif'), reason: 'unresizable' }],
        });
        expect(said).toContain('over 20 MB');
        expect(said).toContain('could not be scaled down');
    });

    it('names the count limit for images that had no room', () => {
        expect(describeRejectedImages({ ...none, overflow: [image()] }))
            .toBe(`Only ${MAX_IMAGES} images per message`);
    });

    it('keeps the count sentence where no file can be attached, and drops the one that would lie', () => {
        const rejected = { ...none, asFiles: [{ file: oversized(), reason: 'oversize' as const }], overflow: [image()] };
        expect(describeRejectedImages(rejected, false)).toBe(`Only ${MAX_IMAGES} images per message`);
    });
});

describe('scaledImageDimensions', () => {
    const cases: { name: string; width: number; height: number; want: { width: number; height: number } | null }[] = [
        { name: 'a photo under the ceiling', width: 4032, height: 3024, want: null },
        { name: 'an image at exactly the ceiling', width: 4000, height: 4000, want: null },
        { name: 'a square over it', width: 5000, height: 5000, want: { width: 4000, height: 4000 } },
        { name: 'a wide strip', width: 20000, height: 900, want: { width: 18856, height: 848 } },
        { name: 'a tall strip', width: 900, height: 20000, want: { width: 848, height: 18856 } },
        // The long side of a strip whose short side would floor to zero is bounded by the ceiling itself.
        { name: 'a one-pixel line', width: 1, height: 40_000_000, want: { width: 1, height: MAX_IMAGE_PIXELS } },
        { name: 'nothing to picture', width: 0, height: 0, want: null },
    ];

    it.each(cases)('scales $name', ({ width, height, want }) => {
        expect(scaledImageDimensions(width, height)).toEqual(want);
    });

    it('answers a pair that fits, stands on the ratio, and never grows the picture', () => {
        for (const [width, height] of [[5000, 5000], [6000, 4000], [400000, 100], [100, 400000], [17, 9_000_000]]) {
            const got = scaledImageDimensions(width, height);
            if (!got) throw new Error(`${width}x${height} is over the ceiling and must be scaled`);
            expect(got.width * got.height).toBeLessThanOrEqual(MAX_IMAGE_PIXELS);
            expect(got.width).toBeLessThanOrEqual(width);
            expect(got.height).toBeLessThanOrEqual(height);
            expect(got.width).toBeGreaterThanOrEqual(1);
            expect(got.height).toBeGreaterThanOrEqual(1);
            // Flooring each side off the same ratio leaves at most a pixel of error on each.
            expect(Math.abs(got.width * height - got.height * width)).toBeLessThanOrEqual(width + height);
        }
    });
});

// jsdom has no DataTransfer constructor.
function transferOf(files: File[], text: string | null = null) {
    if (files.length === 0 && text === null) return null;
    const store = new Map<string, string>();
    if (text !== null) store.set('text/plain', text);
    return {
        files,
        getData: (format: string) => (format === 'text/plain' || format === 'text' ? store.get('text/plain') ?? '' : ''),
    };
}

function pasteOf(files: File[], text: string | null = null) {
    const event = new Event('paste', { bubbles: true, cancelable: true }) as ClipboardEvent;
    Object.defineProperty(event, 'clipboardData', { value: transferOf(files, text) });
    return event;
}

describe('takeTransfer', () => {
    it('leaves a text-only paste to the browser, uncancelled', () => {
        const event = pasteOf([], 'a paragraph');
        expect(takeTransfer(event)).toBeNull();
        expect(event.defaultPrevented).toBe(false);
    });

    it('answers nothing for a clipboard it cannot read', () => {
        const event = new Event('paste', { bubbles: true, cancelable: true }) as ClipboardEvent;
        Object.defineProperty(event, 'clipboardData', { value: null });
        expect(takeTransfer(event)).toBeNull();
    });

    it('cancels an image-only paste, which would otherwise leave a filename behind', () => {
        const event = pasteOf([image('shot.png')]);
        const got = takeTransfer(event);
        expect(got?.images.map(f => f.name)).toEqual(['shot.png']);
        expect(event.defaultPrevented).toBe(true);
    });
    it('leaves a clipboard that carries text as well uncancelled: both arrive', () => {
        const event = pasteOf([image('diagram.png')], 'the error text');
        const got = takeTransfer(event);
        expect(got?.images).toHaveLength(1);
        expect(event.defaultPrevented).toBe(false);
    });

    it('cancels a paste whose only text is the copied file\'s own name', () => {
        const file = new File(['x'], 'run.log', { type: 'text/plain' });
        const event = pasteOf([file], 'run.log');
        expect(takeTransfer(event)?.texts.map(f => f.name)).toEqual(['run.log']);
        expect(event.defaultPrevented).toBe(true);
    });

    it('cancels a paste whose text is the copied file\'s path', () => {
        const file = image('screenshot.png');
        const event = pasteOf([file], '/home/me/screenshot.png');
        expect(takeTransfer(event)?.images).toHaveLength(1);
        expect(event.defaultPrevented).toBe(true);
    });

    it('cancels a paste whose text is nothing but blanks', () => {
        const event = pasteOf([image()], '   ');
        expect(takeTransfer(event)?.images).toHaveLength(1);
        expect(event.defaultPrevented).toBe(true);
    });

    it('leaves a paste that carries a file name and real text together to the browser', () => {
        const file = image('screenshot.png');
        const event = pasteOf([file], 'screenshot.png\nthe error text');
        expect(takeTransfer(event)?.images).toHaveLength(1);
        expect(event.defaultPrevented).toBe(false);
    });

    it('reads the same rule from a drop as from a paste, and sorts all three buckets', () => {
        const event = new Event('drop', { bubbles: true, cancelable: true }) as DragEvent;
        Object.defineProperty(event, 'dataTransfer', {
            value: transferOf([image('shot.png'), new File(['x'], 'run.log', { type: 'text/plain' }), new File([''], 'a.zip', { type: 'application/zip' })]),
        });
        const got = takeTransfer(event);
        expect(got?.images.map(f => f.name)).toEqual(['shot.png']);
        expect(got?.texts.map(f => f.name)).toEqual(['run.log']);
        expect(got?.files.map(f => f.name)).toEqual(['a.zip']);
        expect(event.defaultPrevented).toBe(true);
    });
});

describe('readTextFile', () => {
    it('reads a small document whole and reports nothing dropped', async () => {
        const file = new File([new TextEncoder().encode('the log\nsecond line')], 'a.log', { type: 'text/plain' });
        const { content, droppedBytes } = await readTextFile(file);
        expect(content).toBe('the log\nsecond line');
        expect(droppedBytes).toBe(0);
    });

    it('reads at most the cap out of a huge one and says how much it left out', async () => {
        const file = new File([new Uint8Array(4 * 1024 * 1024)], 'huge.log', { type: 'text/plain' });
        const { content, droppedBytes } = await readTextFile(file);
        expect(new TextEncoder().encode(content).length).toBeLessThanOrEqual(MAX_INLINE_TEXT);
        expect(droppedBytes).toBe(file.size - MAX_INLINE_TEXT);
    });
});

/** A file's bytes as bare base64, read the way the browser reads them. */
function base64Of(file: File): Promise<string> {
    return new Promise((resolve, reject) => {
        const reader = new FileReader();
        reader.onload = () => {
            const url = String(reader.result);
            resolve(url.slice(url.indexOf(',') + 1));
        };
        reader.onerror = () => reject(reader.error);
        reader.readAsDataURL(file);
    });
}

describe('readImageAttachments', () => {
    it('keeps the bytes it was given for an image inside both caps', async () => {
        const first = image('a.png');
        const p = picture(800, 600);
        const picked = await readImageAttachments([first, image('b.png')], MAX_IMAGES, p.decode);
        expect(picked.accepted).toHaveLength(2);
        expect(picked.accepted[0].mimeType).toBe('image/png');
        expect(picked.accepted[0].data.startsWith('data:')).toBe(false);
        expect(picked.accepted[0].data).toBe(await base64Of(first));
        expect(p.asked).toEqual([]);
        expect(p.closedCount()).toBe(2);
    });

    it('resamples an image over the ceiling, in the format it arrived as', async () => {
        const p = picture(5000, 5000, encodedBlob([9, 9]));
        const picked = await readImageAttachments([image('big.png')], MAX_IMAGES, p.decode);
        expect(p.asked).toEqual([{ mime: 'image/png', width: 4000, height: 4000 }]);
        expect(picked.accepted[0].data).toBe('CQk=');
        expect(p.closedCount()).toBe(1);
    });

    it('refuses to keep an image whose format the browser cannot write', async () => {
        const p = picture(5000, 5000, null);
        const picked = await readImageAttachments([image('big.gif', 2, 'image/gif')], MAX_IMAGES, p.decode);
        expect(picked.accepted).toEqual([]);
        expect(picked.asFiles.map((e) => [e.file.name, e.reason])).toEqual([['big.gif', 'unresizable']]);
        expect(p.closedCount()).toBe(1);
    });

    it('sends an image the browser cannot picture to the task as a file', async () => {
        const picked = await readImageAttachments([image('a.png')], MAX_IMAGES, undecodable);
        expect(picked.accepted).toEqual([]);
        expect(picked.asFiles.map((e) => e.reason)).toEqual(['unresizable']);
    });

    it('treats a decoder that throws as the same fallback', async () => {
        const picked = await readImageAttachments(
            [image('a.png')],
            MAX_IMAGES,
            async () => {
                throw new Error('the bitmap refused');
            },
        );
        expect(picked.asFiles.map((e) => e.reason)).toEqual(['unresizable']);
    });

    it('never asks for a picture of a file the byte cap already refused', async () => {
        let asked = 0;
        const decode: DecodeImage = async () => {
            asked += 1;
            return { width: 1, height: 1, encode: async () => null, close: () => {} };
        };
        const picked = await readImageAttachments([oversized()], MAX_IMAGES, decode);
        expect(asked).toBe(0);
        expect(picked.asFiles.map((e) => e.reason)).toEqual(['oversize']);
    });

    it('never reads more than the room it was given', async () => {
        const p = picture(2, 2);
        const picked = await readImageAttachments([image('a.png'), image('b.png'), image('c.png')], 2, p.decode);
        expect(picked.accepted).toHaveLength(2);
        expect(picked.overflow).toHaveLength(1);
    });

    it('takes nothing when nothing is left', async () => {
        const p = picture(2, 2);
        const picked = await readImageAttachments([image()], 0, p.decode);
        expect(picked.accepted).toEqual([]);
        expect(picked.overflow).toHaveLength(1);
        expect(p.asked).toEqual([]);
    });

    it('treats a negative remainder as no room, not as "take everything"', async () => {
        const p = picture(2, 2);
        const picked = await readImageAttachments([image('a.png'), image('b.png')], -3, p.decode);
        expect(picked.accepted).toEqual([]);
        expect(picked.overflow).toHaveLength(2);
    });

    it('costs no slot an image that could not be kept as an image', async () => {
        const small = picture(2, 2);
        const picked = await readImageAttachments(
            [image('broken.png'), image('fine.png')],
            1,
            async (file) => (file.name === 'broken.png' ? null : small.decode(file)),
        );
        expect(picked.accepted.map((a) => a.mimeType)).toEqual(['image/png']);
        expect(picked.asFiles.map((e) => e.file.name)).toEqual(['broken.png']);
    });

    it('ignores a pick that is not an attachable image, which the picker filters too', async () => {
        const p = picture(2, 2);
        const picked = await readImageAttachments(
            [image('a.png'), new File([''], 'a.mp4', { type: 'video/mp4' })],
            MAX_IMAGES,
            p.decode,
        );
        expect(picked.accepted).toHaveLength(1);
        expect(picked.asFiles).toEqual([]);
        expect(picked.overflow).toEqual([]);
    });

    it('round-trips the data URL an <img> is shown from', async () => {
        const p = picture(2, 2);
        const picked = await readImageAttachments(
            [new File([new TextEncoder().encode('ab')], 'ab.png', { type: 'image/png' })],
            MAX_IMAGES,
            p.decode,
        );
        expect(imageSrc(picked.accepted[0])).toBe('data:image/png;base64,YWI=');
    });
});
