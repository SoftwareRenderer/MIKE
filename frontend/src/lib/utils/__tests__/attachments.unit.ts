import { describe, it, expect } from 'vitest';
import {
    describeRejectedImages,
    imageSrc,
    readImageAttachments,
    readTextFile,
    sortTransferFiles,
    takeTransfer,
    MAX_IMAGES,
    MAX_IMAGE_SIZE,
    MAX_INLINE_TEXT,
} from '../attachments';

const KB = 1024;

function image(name = 'shot.png', bytes = 2 * KB, type = 'image/png'): File {
    return new File([new Uint8Array(bytes)], name, { type });
}

// A File whose size is over the cap without allocating 4 MB.
function oversized(name = 'huge.png'): File {
    const f = image(name);
    Object.defineProperty(f, 'size', { value: MAX_IMAGE_SIZE + 1 });
    return f;
}

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
            new File([''], 'no-type'),
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
    it('says nothing when everything was accepted', () => {
        expect(describeRejectedImages({ oversize: [], overflow: [] })).toBeNull();
    });

    it('names the size limit for oversized images', () => {
        expect(describeRejectedImages({ oversize: [oversized()], overflow: [] }))
            .toBe('1 image too large (max 4 MB)');
        expect(describeRejectedImages({ oversize: [oversized(), oversized('b.png')], overflow: [] }))
            .toBe('2 images too large (max 4 MB)');
    });

    it('names the count limit for images that had no room', () => {
        expect(describeRejectedImages({ oversize: [], overflow: [image()] }))
            .toBe(`Only ${MAX_IMAGES} images per message`);
    });

    it('leads with the size problem, which is the one that looks like a bug', () => {
        expect(describeRejectedImages({ oversize: [oversized()], overflow: [image()] }))
            .toContain('too large');
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

    it('owns an image-only clipboard, which would otherwise leave a filename behind', () => {
        const event = pasteOf([image('shot.png')]);
        const got = takeTransfer(event);
        expect(got?.images.map(f => f.name)).toEqual(['shot.png']);
        expect(got?.owns).toBe(true);
        expect(event.defaultPrevented).toBe(true);
    });
    it('never owns a clipboard that carries text as well: both arrive', () => {
        const event = pasteOf([image('diagram.png')], 'the error text');
        const got = takeTransfer(event);
        expect(got?.images).toHaveLength(1);
        expect(got?.owns).toBe(false);
        expect(event.defaultPrevented).toBe(false);
    });

    it('owns a paste whose only text is the copied file\'s own name', () => {
        const file = new File(['x'], 'run.log', { type: 'text/plain' });
        const event = pasteOf([file], 'run.log');
        expect(takeTransfer(event)?.owns).toBe(true);
        expect(event.defaultPrevented).toBe(true);
    });

    it('owns a paste whose text is the copied file\'s path', () => {
        const file = image('screenshot.png');
        expect(takeTransfer(pasteOf([file], '/home/me/screenshot.png'))?.owns).toBe(true);
    });

    it('owns a paste whose text is nothing but blanks', () => {
        expect(takeTransfer(pasteOf([image()], '   '))?.owns).toBe(true);
    });

    it('never owns a paste that carries a file name and real text together', () => {
        const file = image('screenshot.png');
        expect(takeTransfer(pasteOf([file], 'screenshot.png\nthe error text'))?.owns).toBe(false);
    });

    it('cancels synchronously, before the caller can await anything', () => {
        const event = pasteOf([image('shot.png')]);
        takeTransfer(event);
        expect(event.defaultPrevented).toBe(true);
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

describe('readImageAttachments', () => {
    it('reads accepted images as bare base64, in pick order', async () => {
        const picked = await readImageAttachments([image('a.png'), image('b.png')], MAX_IMAGES);
        expect(picked.accepted).toHaveLength(2);
        expect(picked.accepted[0].mimeType).toBe('image/png');
        expect(picked.accepted[0].data.startsWith('data:')).toBe(false);
    });

    it('never reads more than the room it was given', async () => {
        const picked = await readImageAttachments([image('a.png'), image('b.png'), image('c.png')], 2);
        expect(picked.accepted).toHaveLength(2);
        expect(picked.overflow).toHaveLength(1);
    });

    it('takes nothing when nothing is left', async () => {
        const picked = await readImageAttachments([image()], 0);
        expect(picked.accepted).toEqual([]);
        expect(picked.overflow).toHaveLength(1);
    });

    it('treats a negative remainder as no room, not as "take everything"', async () => {
        const picked = await readImageAttachments([image('a.png'), image('b.png')], -3);
        expect(picked.accepted).toEqual([]);
        expect(picked.overflow).toHaveLength(2);
    });

    it('ignores a pick that is not an attachable image, which the picker filters too', async () => {
        const picked = await readImageAttachments([image('a.png'), new File([''], 'a.mp4', { type: 'video/mp4' })], MAX_IMAGES);
        expect(picked.accepted).toHaveLength(1);
        expect(picked.oversize).toEqual([]);
        expect(picked.overflow).toEqual([]);
    });

    it('round-trips the data URL an <img> is shown from', async () => {
        const picked = await readImageAttachments(
            [new File([new TextEncoder().encode('ab')], 'ab.png', { type: 'image/png' })],
            MAX_IMAGES,
        );
        expect(imageSrc(picked.accepted[0])).toBe('data:image/png;base64,YWI=');
    });

    it('reads nothing but reports an oversized pick', async () => {
        const picked = await readImageAttachments([oversized()], MAX_IMAGES);
        expect(picked.accepted).toEqual([]);
        expect(picked.oversize).toHaveLength(1);
    });
});
