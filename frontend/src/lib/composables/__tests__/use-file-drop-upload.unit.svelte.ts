// @vitest-environment jsdom
// Unit tests for the file-list drop zone: progress, cancellation, and the single
// overwrite question a folder drop asks.
// (.svelte.ts extension: the composable under test uses $state.)
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { setCsrfToken } from '$lib/api';
import { dialogStore } from '$lib/stores';
import { useFileDropUpload } from '../use-file-drop-upload.svelte';

/**
 * A stand-in XHR the test drives by hand: uploads are byte streams with their own
 * lifecycle, and the behaviour under test (progress, abort, 409 vs 500) is exactly
 * the part a network-backed mock could not time deterministically.
 */
class FakeXHR {
    static sent: FakeXHR[] = [];
    static responder: (xhr: FakeXHR, relPath: string) => void = (xhr) => xhr.finish(200, '{"status":"written"}');

    status = 0;
    responseText = '';
    withCredentials = false;
    aborted = false;
    method = '';
    url = '';
    headers: Record<string, string> = {};
    form: FormData | null = null;
    upload: { onprogress?: (e: { loaded: number; total: number }) => void } = {};
    onload: (() => void) | null = null;
    onerror: (() => void) | null = null;
    onabort: (() => void) | null = null;

    open(method: string, url: string) {
        this.method = method;
        this.url = url;
    }
    setRequestHeader(key: string, value: string) {
        this.headers[key] = value;
    }
    send(form: FormData) {
        this.form = form;
        FakeXHR.sent.push(this);
        queueMicrotask(() => {
            if (!this.aborted) FakeXHR.responder(this, String(form.get('relpath')));
        });
    }
    abort() {
        if (this.aborted) return;
        this.aborted = true;
        this.onabort?.();
    }

    // Driven by the responder
    progress(loaded: number, total: number) {
        this.upload.onprogress?.({ loaded, total });
    }
    finish(status: number, body = '') {
        this.status = status;
        this.responseText = body;
        this.onload?.();
    }
}

const DIR = '/home/sandbox/demo';

/** A dropped tree: names → files or nested folders, exactly as the drop described. */
interface FakeTree {
    [name: string]: File | FakeTree;
}

function treeOf(files: Array<{ relPath: string; size: number }>): FakeTree {
    const root: FakeTree = {};
    for (const { relPath, size } of files) {
        const parts = relPath.split('/');
        let level = root;
        for (const dir of parts.slice(0, -1)) {
            const existing = level[dir];
            if (!existing || existing instanceof File) {
                const folder: FakeTree = {};
                level[dir] = folder;
                level = folder;
            } else {
                level = existing;
            }
        }
        const file = new File(['x'], parts[parts.length - 1]);
        Object.defineProperty(file, 'size', { value: size });
        level[parts[parts.length - 1]] = file;
    }
    return root;
}

function entryOf(name: string, node: File | FakeTree): unknown {
    if (node instanceof File) {
        return { isFile: true, isDirectory: false, name, file: (ok: (f: File) => void) => ok(node) };
    }
    const children = Object.entries(node).map(([childName, child]) => entryOf(childName, child));
    return {
        isFile: false,
        isDirectory: true,
        name,
        createReader: () => {
            let cursor = 0;
            return {
                readEntries: (ok: (entries: unknown[]) => void) => {
                    const batch = children.slice(cursor);
                    cursor = children.length;
                    ok(batch);
                },
            };
        },
    };
}

/** A file as an `<input type="file">` would report it: a bare name, a known size. */
function pickedFile(name: string, size = 10): File {
    const file = new File(['x'], name);
    Object.defineProperty(file, 'size', { value: size });
    return file;
}

function droppedFilesEvent(files: Array<{ relPath: string; size?: number }>): DragEvent {
    const tree = treeOf(files.map((f) => ({ relPath: f.relPath, size: f.size ?? 10 })));
    const items = Object.entries(tree).map(([name, node]) => ({
        kind: 'file',
        webkitGetAsEntry: () => entryOf(name, node),
    }));
    return {
        type: 'drop',
        dataTransfer: { types: ['Files'], items, files: [] },
        preventDefault: vi.fn(),
    } as unknown as DragEvent;
}

function harness(files: Array<{ relPath: string; size?: number }>) {
    const onUploaded = vi.fn();
    const drop = useFileDropUpload({
        projectId: () => 'proj-1',
        dir: () => DIR,
        onUploaded,
    });
    const event = droppedFilesEvent(files);
    return { drop, event, onUploaded };
}

const toasts = () => vi.mocked(dialogStore.showToast).mock.calls.map(([message]) => message).join(' | ');

describe('useFileDropUpload', () => {
    beforeEach(() => {
        FakeXHR.sent = [];
        FakeXHR.responder = (xhr) => xhr.finish(200, '{"status":"written"}');
        vi.stubGlobal('XMLHttpRequest', FakeXHR);
        setCsrfToken('csrf-test');
        vi.spyOn(dialogStore, 'showToast').mockImplementation(() => {});
        vi.spyOn(dialogStore, 'showConfirm').mockResolvedValue(true);
    });

    afterEach(() => {
        vi.unstubAllGlobals();
        vi.restoreAllMocks();
    });

    it('uploads each dropped file into the directory the list shows', async () => {
        const { drop, event, onUploaded } = harness([{ relPath: 'a.txt' }, { relPath: 'b.txt', size: 40 }]);
        drop.handleDrop(event);

        await vi.waitFor(() => expect(FakeXHR.sent).toHaveLength(2));
        await vi.waitFor(() => expect(drop.uploading).toBe(false));

        for (const xhr of FakeXHR.sent) {
            expect(xhr.url).toBe('/api/fs/upload?project=proj-1');
            expect(xhr.headers['X-CSRF-Token']).toBe('csrf-test');
            expect(xhr.withCredentials).toBe(true);
            expect(xhr.form?.get('path')).toBe(DIR);
        }
        expect(FakeXHR.sent.map((xhr) => xhr.form?.get('relpath')).sort()).toEqual(['a.txt', 'b.txt']);
        expect(onUploaded).toHaveBeenCalledWith(expect.arrayContaining(['a.txt', 'b.txt']));
        expect(toasts()).toBe('Uploaded 2 files');
        expect(vi.mocked(dialogStore.showToast).mock.calls[0][1]).toBe('success');
        expect(event.preventDefault).toHaveBeenCalled();
    });

    it('reports byte progress while the upload runs', async () => {
        FakeXHR.responder = () => {}; // nothing answers until the test drives the bytes
        const { drop, event } = harness([{ relPath: 'a.txt', size: 10 }, { relPath: 'b.txt', size: 40 }]);
        drop.handleDrop(event);

        await vi.waitFor(() => expect(FakeXHR.sent).toHaveLength(2));
        expect(drop.filesTotal).toBe(2);
        expect(drop.bytesTotal).toBe(50);
        expect(drop.percent).toBe(0);

        FakeXHR.sent[0].progress(5, 10);
        expect(drop.bytesDone).toBe(5);
        expect(drop.percent).toBe(10);
        expect(drop.currentFile).toBeTruthy();

        FakeXHR.sent[1].progress(25, 40);
        expect(drop.bytesDone).toBe(30); // both transfers count toward one bar
        expect(drop.percent).toBe(60);

        FakeXHR.sent.forEach((xhr) => xhr.finish(200));
        await vi.waitFor(() => expect(drop.uploading).toBe(false));
        expect(drop.percent).toBe(100);
        expect(drop.filesDone).toBe(2);
    });

    it('asks once about every clashing name and retries them as an overwrite', async () => {
        FakeXHR.responder = (xhr, relPath) => {
            const overwriting = xhr.form?.get('overwrite') === '1';
            if (relPath === 'a.txt' && !overwriting) xhr.finish(409, '{"error":"file already exists: a.txt"}');
            else xhr.finish(200);
        };
        const { drop, event, onUploaded } = harness([{ relPath: 'a.txt' }, { relPath: 'b.txt' }]);
        drop.handleDrop(event);

        await vi.waitFor(() => expect(dialogStore.showConfirm).toHaveBeenCalledTimes(1));
        const question = vi.mocked(dialogStore.showConfirm).mock.calls[0][0];
        expect(question.items).toEqual(['a.txt']);
        expect(question.message).toContain('1 file is already here');

        await vi.waitFor(() => expect(drop.uploading).toBe(false));
        expect(vi.mocked(dialogStore.showConfirm)).toHaveBeenCalledTimes(1); // one question per drop
        const retry = FakeXHR.sent.filter((xhr) => xhr.form?.get('overwrite') === '1');
        expect(retry.map((xhr) => xhr.form?.get('relpath'))).toEqual(['a.txt']);
        expect(onUploaded).toHaveBeenCalledTimes(1);
        expect(onUploaded).toHaveBeenCalledWith(expect.arrayContaining(['a.txt', 'b.txt']));
        expect(toasts()).toBe('Uploaded 2 files');
    });

    it('declining the overwrite question aborts the whole drop', async () => {
        FakeXHR.responder = (xhr, relPath) => {
            if (relPath === 'a.txt') xhr.finish(409, '{"error":"file already exists"}');
            else xhr.finish(200);
        };
        vi.mocked(dialogStore.showConfirm).mockResolvedValue(false);
        const { drop, event, onUploaded } = harness([{ relPath: 'a.txt' }, { relPath: 'b.txt' }]);
        drop.handleDrop(event);

        await vi.waitFor(() => expect(drop.uploading).toBe(false));
        // Nothing after the refusal was attempted: saying no means stop.
        expect(FakeXHR.sent).toHaveLength(2);
        expect(FakeXHR.sent.some((xhr) => xhr.form?.get('overwrite') === '1')).toBe(false);
        // What already landed is reported as written, not silently kept.
        expect(onUploaded).toHaveBeenCalledWith(['b.txt']);
        expect(toasts()).toContain('Upload cancelled — 1 file uploaded');
        expect(vi.mocked(dialogStore.showToast).mock.calls[0][1]).toBe('error');
    });

    it('cancel stops what is in flight and starts nothing else', async () => {
        FakeXHR.responder = () => {}; // nothing answers until the run is cancelled
        const many = Array.from({ length: 6 }, (_, i) => ({ relPath: `f${i}.txt` }));
        const { drop, event, onUploaded } = harness(many);
        drop.handleDrop(event);

        // The concurrency cap is what "nothing else started" is measured against.
        await vi.waitFor(() => expect(FakeXHR.sent.length).toBeGreaterThan(0));
        await vi.waitFor(() => expect(FakeXHR.sent).toHaveLength(4));
        expect(drop.uploading).toBe(true);

        drop.cancel();
        await vi.waitFor(() => expect(drop.uploading).toBe(false));

        expect(FakeXHR.sent).toHaveLength(4);
        expect(FakeXHR.sent.every((xhr) => xhr.aborted)).toBe(true);
        expect(onUploaded).not.toHaveBeenCalled();
        expect(toasts()).toContain('Upload cancelled');
    });

    it('names the server reason when a file is refused', async () => {
        FakeXHR.responder = (xhr) => xhr.finish(500, '{"error":"write file failed"}');
        const { drop, event, onUploaded } = harness([{ relPath: 'a.txt' }]);
        drop.handleDrop(event);

        await vi.waitFor(() => expect(drop.uploading).toBe(false));
        expect(toasts()).toBe('1 file was not uploaded: write file failed');
        expect(vi.mocked(dialogStore.showToast).mock.calls[0][1]).toBe('error');
        expect(onUploaded).not.toHaveBeenCalled();
        expect(drop.filesDone).toBe(0);
    });

    it('sends a task-scoped drop to that task\'s worktree', async () => {
        const drop = useFileDropUpload({
            projectId: () => 'proj-1',
            taskId: () => 'task-9',
            dir: () => '/home/sandbox/demo/.worktrees/task-9',
            onUploaded: vi.fn(),
        });
        drop.handleDrop(droppedFilesEvent([{ relPath: 'a.txt' }]));

        await vi.waitFor(() => expect(FakeXHR.sent).toHaveLength(1));
        // The `task` parameter is what /api/fs/upload resolves the target through;
        // without it the drop would land on the project's own checkout.
        expect(FakeXHR.sent[0].url).toBe('/api/fs/upload?project=proj-1&task=task-9');
    });

    it('the file picker feeds the same run as a drop', async () => {
        const onUploaded = vi.fn();
        const drop = useFileDropUpload({ projectId: () => 'proj-1', dir: () => DIR, onUploaded });
        const picked = [pickedFile('notes.txt', 10), pickedFile('img.png', 20)];
        // Through the change handler an <input> actually calls, not uploadFiles directly:
        // clearing the input is half of what a picker has to get right.
        const input = { files: picked, value: 'notes.txt' };
        drop.handlePicked({ target: input } as unknown as Event);

        await vi.waitFor(() => expect(FakeXHR.sent).toHaveLength(2));
        await vi.waitFor(() => expect(drop.uploading).toBe(false));
        // A value left in the input means picking the same file twice fires change once,
        // and the second pick looks like one the app ignored.
        expect(input.value).toBe('');
        expect(FakeXHR.sent.map((xhr) => xhr.form?.get('relpath')).sort()).toEqual(['img.png', 'notes.txt']);
        // A picker names files, not folders, and the browser has already stripped any
        // path from File.name — so these land in the directory the list shows.
        expect(FakeXHR.sent.every((xhr) => xhr.form?.get('path') === DIR)).toBe(true);
        expect(onUploaded).toHaveBeenCalledWith(expect.arrayContaining(['notes.txt', 'img.png']));
        expect(toasts()).toBe('Uploaded 2 files');
    });

    it('ignores a second run while one is already in flight', async () => {
        FakeXHR.responder = () => {}; // nothing answers, so the run stays open
        const drop = useFileDropUpload({ projectId: () => 'proj-1', dir: () => DIR, onUploaded: vi.fn() });

        drop.uploadFiles([pickedFile('a.txt')]);
        await vi.waitFor(() => expect(FakeXHR.sent).toHaveLength(1));

        drop.uploadFiles([pickedFile('b.txt')]);
        drop.handleDrop(droppedFilesEvent([{ relPath: 'c.txt' }]));
        await Promise.resolve();

        expect(FakeXHR.sent).toHaveLength(1);
        expect(drop.uploading).toBe(true);

        drop.cancel();
        await vi.waitFor(() => expect(drop.uploading).toBe(false));
        expect(FakeXHR.sent).toHaveLength(1);
    });

    it('refuses a picked file over the byte cap without sending it', async () => {
        const drop = useFileDropUpload({ projectId: () => 'proj-1', dir: () => DIR, onUploaded: vi.fn() });
        drop.uploadFiles([pickedFile('huge.zip', 25 * 1024 * 1024), pickedFile('ok.txt', 5)]);

        await vi.waitFor(() => expect(drop.uploading).toBe(false));
        expect(FakeXHR.sent.map((xhr) => xhr.form?.get('relpath'))).toEqual(['ok.txt']);
        expect(toasts()).toContain('over 20 MB');
        expect(toasts()).toContain('Uploaded 1 file');
    });

    it('leaves a drag that carries no files alone', () => {
        const { drop } = harness([{ relPath: 'a.txt' }]);
        const boardDrag = {
            type: 'drop',
            dataTransfer: { types: ['text/plain'] },
            preventDefault: vi.fn(),
        } as unknown as DragEvent;

        drop.handleDragOver(boardDrag);
        drop.handleDrop(boardDrag);

        expect(boardDrag.preventDefault).not.toHaveBeenCalled();
        expect(drop.isOver).toBe(false);
        expect(FakeXHR.sent).toHaveLength(0);
    });

    it('skips files over the byte cap without sending them', async () => {
        const { drop, event } = harness([{ relPath: 'huge.bin', size: 21 * 1024 * 1024 }]);
        drop.handleDrop(event);

        await vi.waitFor(() => expect(drop.uploading).toBe(false));
        expect(FakeXHR.sent).toHaveLength(0);
        expect(toasts()).toContain('over 20 MB');
    });
});
