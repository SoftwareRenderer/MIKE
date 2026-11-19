// @vitest-environment jsdom
// The three buckets one gesture can carry, and what each one does to the field.
// (.svelte.ts extension: the composable under test uses $state.)
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { dialogStore } from '$lib/stores';
import { MAX_IMAGES, MAX_IMAGE_SIZE, MAX_INLINE_TEXT, type DecodeImage } from '$lib/utils/attachments';
import { useImageAttachments } from '../use-image-attachments.svelte';

function pasteOf(files: File[], text: string | null = null) {
    const event = new Event('paste', { bubbles: true, cancelable: true }) as ClipboardEvent;
    Object.defineProperty(event, 'clipboardData', {
        value: {
            files,
            getData: (format: string) =>
                (format === 'text/plain' || format === 'text') && text !== null ? text : '',
        },
    });
    return event;
}

function dropOf(files: File[]) {
    const event = new Event('drop', { bubbles: true, cancelable: true }) as DragEvent;
    Object.defineProperty(event, 'dataTransfer', { value: { files, types: ['Files'] } });
    return event;
}

function dragOf(types: string[], name: 'dragenter' | 'dragover' | 'dragleave' = 'dragenter') {
    const event = new Event(name, { bubbles: true, cancelable: true }) as DragEvent;
    Object.defineProperty(event, 'dataTransfer', { value: { files: [], types } });
    return event;
}

const png = (name = 'shot.png') => new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], name, { type: 'image/png' });
const textFile = (name: string, body: string) => new File([body], name, { type: 'text/plain' });
const archive = (name = 'bundle.zip') => new File([new Uint8Array([0x50, 0x4b, 0x03, 0x04])], name, { type: 'application/zip' });

/**
 * jsdom has no canvas, so files are pictured at these dimensions unless the test brings its own decoder.
 * `writes` is what a resample returns; these fixtures stay under the ceiling, so it answers nothing by default.
 */
function pictureOf(width: number, height: number, writes: Blob | null = null): DecodeImage {
    return async () => ({ width, height, encode: async () => writes, close: () => {} });
}

function setup(options: { upload?: boolean; hasTask?: boolean; held?: number; decode?: DecodeImage } = {}) {
    const textarea = document.createElement('textarea');
    document.body.append(textarea);
    textarea.value = 'before ';
    textarea.selectionStart = textarea.selectionEnd = textarea.value.length;

    const images: unknown[] = new Array(options.held ?? 0).fill({ mimeType: 'image/png', data: 'AAAA' });
    const uploaded: string[] = [];
    const toasts = vi.spyOn(dialogStore, 'showToast').mockImplementation(() => {});

    const intake = useImageAttachments({
        getTextarea: () => textarea,
        getValue: () => textarea.value,
        setValue: (v) => { textarea.value = v; },
        held: () => images.length,
        addImages: (accepted) => { images.push(...accepted); },
        uploadFile: options.upload === false
            ? undefined
            : async (file) => { uploaded.push(file.name); return `/home/sandbox/p1/.mike-uploads/${file.name}`; },
        hasTask: () => options.hasTask !== false,
        decode: options.decode ?? pictureOf(100, 100),
    });

    return { textarea, intake, images, uploaded, toasts };
}

beforeEach(() => {
    document.body.innerHTML = '';
    vi.restoreAllMocks();
});

describe('paste', () => {
    it('attaches the image of a rich clipboard and leaves its text to the browser', async () => {
        const h = setup();
        const event = pasteOf([png()], 'the stack trace is in this');

        h.intake.handlePaste(event);

        await vi.waitFor(() => expect(h.images).toHaveLength(1));
        expect(event.defaultPrevented).toBe(false);
        expect(h.textarea.value).toBe('before ');
    });

    it('cancels the insertion when the clipboard carries an image and nothing the user would miss', async () => {
        const h = setup();
        const event = pasteOf([png()], 'shot.png'); // some platforms put the file's own name there

        h.intake.handlePaste(event);

        await vi.waitFor(() => expect(h.images).toHaveLength(1));
        expect(event.defaultPrevented).toBe(true);
    });

    it('leaves a text-only paste to the browser', async () => {
        const h = setup();
        const event = pasteOf([], 'a paragraph');

        h.intake.handlePaste(event);

        expect(event.defaultPrevented).toBe(false);
        await vi.waitFor(() => expect(h.toasts).not.toHaveBeenCalled());
        expect(h.images).toHaveLength(0);
    });

    it('inserts at the cursor, not at the end of what is already there', async () => {
        const h = setup();
        h.textarea.value = 'head  tail';
        h.textarea.selectionStart = h.textarea.selectionEnd = 5;

        h.intake.handlePaste(pasteOf([textFile('note.txt', 'contents')]));

        await vi.waitFor(() => expect(h.textarea.value).toContain('contents'));
        expect(h.textarea.value.startsWith('head ')).toBe(true);
        expect(h.textarea.value.endsWith(' tail')).toBe(true);
    });

    it('names the file it inlined, and says what it left out past the cap', async () => {
        const h = setup();
        const huge = 'x'.repeat(MAX_INLINE_TEXT + 5000);

        h.intake.handlePaste(pasteOf([textFile('huge.log', huge)]));

        await vi.waitFor(() => expect(h.textarea.value).toContain('huge.log'));
        expect(h.textarea.value).toContain('```');
        expect(h.textarea.value).toContain('not included');
        expect((h.textarea.value.match(/x/g) ?? []).length).toBe(MAX_INLINE_TEXT);
    });

    it('writes a file that is neither image nor text and names the path it landed at', async () => {
        const h = setup();

        h.intake.handlePaste(pasteOf([archive()]));

        await vi.waitFor(() => expect(h.uploaded).toEqual(['bundle.zip']));
        expect(h.textarea.value).toContain('/home/sandbox/p1/.mike-uploads/bundle.zip');
    });

    it('reports an upload that failed instead of dropping the file quietly', async () => {
        const textarea = document.createElement('textarea');
        const toasts = vi.spyOn(dialogStore, 'showToast').mockImplementation(() => {});
        const intake = useImageAttachments({
            getTextarea: () => textarea,
            getValue: () => textarea.value,
            setValue: (v) => { textarea.value = v; },
            held: () => 0,
            addImages: () => {},
            uploadFile: async () => { throw new Error('the task’s sandbox is not running'); },
            hasTask: () => true,
        });

        intake.handlePaste(pasteOf([archive()]));

        await vi.waitFor(() => expect(toasts).toHaveBeenCalled());
        expect(toasts.mock.calls[0][0]).toContain('sandbox is not running');
        expect(textarea.value).toBe('');
    });

    it('writes nothing where there is no task environment, and says so', async () => {
        const h = setup({ upload: false, hasTask: false });

        h.intake.handlePaste(pasteOf([archive()]));

        await vi.waitFor(() => expect(h.toasts).toHaveBeenCalled());
        expect(h.toasts.mock.calls[0][0]).toMatch(/task/i);
        expect(h.uploaded).toEqual([]);
        expect(h.textarea.value).toBe('before ');
    });

    it('takes the files of a clipboard that carries text too, and leaves that text to the browser', async () => {
        const h = setup();
        const event = pasteOf([png(), textFile('notes.md', 'read me'), archive('data.db')], 'a caption');

        h.intake.handlePaste(event);

        expect(event.defaultPrevented).toBe(false);
        await vi.waitFor(() => expect(h.uploaded).toEqual(['data.db']));
        expect(h.images).toHaveLength(1);
        expect(h.textarea.value).toContain('read me');
        expect(h.textarea.value).toContain('data.db');
    });
});

describe('image caps', () => {
    it('reports images that had no room instead of dropping them', async () => {
        const h = setup({ held: MAX_IMAGES });

        h.intake.handlePaste(pasteOf([png('one.png')]));

        await vi.waitFor(() => expect(h.toasts).toHaveBeenCalled());
        expect(h.toasts.mock.calls[0][0]).toContain(`Only ${MAX_IMAGES} images`);
        expect(h.images).toHaveLength(MAX_IMAGES);
    });

    it('keeps an image as large as the upload cap allows, which is what an attached file may be', async () => {
        const h = setup();
        const big = new File([new Uint8Array(5 * 1024 * 1024)], 'big.png', { type: 'image/png' });

        h.intake.handlePaste(pasteOf([big]));

        await vi.waitFor(() => expect(h.images).toHaveLength(1));
        expect(h.uploaded).toEqual([]);
        expect(h.toasts).not.toHaveBeenCalled();
    });

    it('uploads an image over the byte cap into the task and says it became a file', async () => {
        const h = setup();
        const huge = png('huge.png');
        Object.defineProperty(huge, 'size', { value: MAX_IMAGE_SIZE + 1 });

        h.intake.handlePaste(pasteOf([huge]));

        await vi.waitFor(() => expect(h.uploaded).toEqual(['huge.png']));
        expect(h.images).toHaveLength(0);
        expect(h.toasts.mock.calls.flat().join(' ')).toContain('attached as a file');
    });

    it('uploads an image no browser can picture, naming the resample as the reason', async () => {
        const h = setup({ decode: async () => null });

        h.intake.handlePaste(pasteOf([png('broken.png')]));

        await vi.waitFor(() => expect(h.uploaded).toEqual(['broken.png']));
        expect(h.toasts.mock.calls.flat().join(' ')).toContain('could not be scaled down');
    });

    it('scales an image over the pixel ceiling down rather than refusing it', async () => {
        const h = setup({ decode: pictureOf(5000, 5000, new Blob([new Uint8Array([9, 9])], { type: 'image/png' })) });

        h.intake.handlePaste(pasteOf([png('huge-shot.png')]));

        await vi.waitFor(() => expect(h.images).toHaveLength(1));
        expect(h.images[0]).toEqual({ mimeType: 'image/png', data: 'CQk=' });
        expect(h.uploaded).toEqual([]);
        expect(h.toasts).not.toHaveBeenCalled();
    });

    it('names the missing task rather than claiming an upload for a surface that has nowhere to write', async () => {
        const h = setup({ hasTask: false });
        const huge = png('huge.png');
        Object.defineProperty(huge, 'size', { value: MAX_IMAGE_SIZE + 1 });

        h.intake.handlePaste(pasteOf([huge]));

        await vi.waitFor(() => expect(h.toasts).toHaveBeenCalled());
        const said = h.toasts.mock.calls.flat().join(' ');
        expect(said).toContain('needs a running task');
        expect(said).not.toContain('attached as a file');
    });

    it('counts what the field already holds, so two gestures cannot exceed the cap', async () => {
        const h = setup({ held: MAX_IMAGES - 1 });

        h.intake.handlePaste(pasteOf([png('a.png'), png('b.png')]));

        await vi.waitFor(() => expect(h.images).toHaveLength(MAX_IMAGES));
        expect(h.toasts.mock.calls.flat().join(' ')).toContain('Only');
    });
});

describe('the picker', () => {
    function pickedOn(files: File[]) {
        const input = document.createElement('input');
        input.value = 'C:\\fakepath\\whatever';
        Object.defineProperty(input, 'files', { value: files });
        const event = new Event('change');
        Object.defineProperty(event, 'target', { value: input });
        return { input, event };
    }

    it('sorts a picked file through the same buckets as a paste', async () => {
        const h = setup();
        const { event } = pickedOn([archive('picked.bin')]);

        h.intake.handlePicked(event);

        await vi.waitFor(() => expect(h.uploaded).toEqual(['picked.bin']));
    });

    it('clears the input so picking the same file twice works twice', async () => {
        const h = setup();
        const { input, event } = pickedOn([png()]);

        h.intake.handlePicked(event);

        expect(input.value).toBe('');
    });
});

describe('the drop ring', () => {
    it('stays on through a dragleave over a child of the zone', () => {
        const h = setup();

        h.intake.handleDragEnter(dragOf(['Files']));
        expect(h.intake.isOver).toBe(true);

        h.intake.handleDragLeave(dragOf(['Files'], 'dragleave'));
        expect(h.intake.isOver).toBe(false);

        h.intake.handleDragEnter(dragOf(['Files']));
        h.intake.handleDragEnter(dragOf(['Files'], 'dragenter')); // crossed into a child
        h.intake.handleDragLeave(dragOf(['Files'], 'dragleave')); // and back out of it
        expect(h.intake.isOver).toBe(true);
    });

    it('sorts a dropped set into the same three buckets', async () => {
        const h = setup();

        h.intake.handleDrop(dropOf([png('dropped.png'), archive('x.db')]));

        await vi.waitFor(() => expect(h.uploaded).toEqual(['x.db']));
        expect(h.images).toHaveLength(1);
        expect(h.intake.isOver).toBe(false);
    });
});
