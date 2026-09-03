import { describe, it, expect } from 'vitest';
import {
    classifyImages,
    describeRejectedImages,
    isImageFile,
    readImageAttachments,
    readImageDataUrl,
    MAX_IMAGES,
    MAX_IMAGE_SIZE,
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

describe('isImageFile', () => {
    it('takes anything the browser reports as an image', () => {
        expect(isImageFile(image())).toBe(true);
        expect(isImageFile(new File([''], 'a.svg', { type: 'image/svg+xml' }))).toBe(true);
    });

    it('rejects everything else', () => {
        expect(isImageFile(new File([''], 'notes.txt', { type: 'text/plain' }))).toBe(false);
        expect(isImageFile(new File([''], 'no-type'))).toBe(false);
    });
});

describe('classifyImages', () => {
    it('keeps images that fit and are within the count cap', () => {
        const { readable, oversize, overflow } = classifyImages([image('a.png'), image('b.png')], MAX_IMAGES);
        expect(readable.map(f => f.name)).toEqual(['a.png', 'b.png']);
        expect(oversize).toEqual([]);
        expect(overflow).toEqual([]);
    });

    it('drops non-images without reporting them — the picker itself filters those', () => {
        const { readable, oversize, overflow } = classifyImages([image('a.png'), new File([''], 'a.mp4', { type: 'video/mp4' })], MAX_IMAGES);
        expect(readable.map(f => f.name)).toEqual(['a.png']);
        expect(oversize).toEqual([]);
        expect(overflow).toEqual([]);
    });

    it('reports an image over the size limit instead of dropping it silently', () => {
        const { readable, oversize } = classifyImages([image('ok.png'), oversized('huge.png')], MAX_IMAGES);
        expect(readable.map(f => f.name)).toEqual(['ok.png']);
        expect(oversize.map(f => f.name)).toEqual(['huge.png']);
    });

    it('reports what had no room once the existing attachments are counted', () => {
        const picked = [image('a.png'), image('b.png'), image('c.png')];
        const { readable, overflow } = classifyImages(picked, 1);
        expect(readable.map(f => f.name)).toEqual(['a.png']);
        expect(overflow.map(f => f.name)).toEqual(['b.png', 'c.png']);
    });

    it('takes nothing when nothing is left', () => {
        const { readable, overflow } = classifyImages([image()], 0);
        expect(readable).toEqual([]);
        expect(overflow).toHaveLength(1);
    });

    it('treats a negative remainder as no room, not as "take everything"', () => {
        const { readable } = classifyImages([image(), image()], -3);
        expect(readable).toEqual([]);
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

describe('readImageAttachments', () => {
    it('reads accepted images as data URLs, in pick order', async () => {
        const picked = await readImageAttachments([image('a.png'), image('b.png')], MAX_IMAGES);
        expect(picked.accepted).toHaveLength(2);
        expect(picked.accepted[0].mimeType).toBe('image/png');
        expect(picked.accepted[0].data.startsWith('data:image/png;base64,')).toBe(true);
    });

    it('never reads more than the room it was given', async () => {
        const picked = await readImageAttachments([image('a.png'), image('b.png'), image('c.png')], 2);
        expect(picked.accepted).toHaveLength(2);
        expect(picked.overflow).toHaveLength(1);
    });

    it('reads nothing but reports an oversized pick', async () => {
        const picked = await readImageAttachments([oversized()], MAX_IMAGES);
        expect(picked.accepted).toEqual([]);
        expect(picked.oversize).toHaveLength(1);
    });
});

describe('readImageDataUrl', () => {
    it('resolves with the file content encoded', async () => {
        const file = new File([new TextEncoder().encode('ab')], 'ab.png', { type: 'image/png' });
        const url = await readImageDataUrl(file);
        expect(url).toBe('data:image/png;base64,YWI=');
    });
});
