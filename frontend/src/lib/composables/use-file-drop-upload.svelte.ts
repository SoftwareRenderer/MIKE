import { uploadFsFile, type UploadResult } from '$lib/api/fs-upload';
import { dialogStore } from '$lib/stores';
import { t } from '$lib/i18n';
import { collectDroppedFiles, collectPickedFiles, MAX_DROP_FILES, MAX_UPLOAD_BYTES, type DroppedFile, type DroppedSelection } from '$lib/utils/file-drop';
import { useFileDropZone } from './use-file-drop-zone.svelte';

/**
 * The drop zone of a file list: files or whole folders dragged in are written into the
 * directory that list shows. The parts that are easy to get wrong:
 *
 *  - Depth counting: `dragleave` fires when the pointer crosses a child element, so an
 *    overlay that hides on the first leave blinks on every row.
 *  - Files only: in-app drags carry `text/plain` and must neither light the zone up nor
 *    be swallowed by it.
 *  - One question per drop: the server answers 409 per existing name, so a folder drop
 *    asks once for every clash. Answering "no" aborts the drop; what is already written
 *    stays written and the toast says so.
 *  - Cancellation aborts every request in flight and starts nothing further.
 */

/** Files uploaded in parallel. */
const UPLOAD_CONCURRENCY = 4;

export interface FileDropUploadOptions {
    projectId: () => string;
    /** Task worktree to upload into; omit for the project's own checkout. */
    taskId?: () => string;
    /** The directory the list is showing, in the form the browser shows it. */
    dir: () => string;
    /** Called once per run that wrote anything, so the listing can refresh. */
    onUploaded: (relPaths: string[]) => void;
}

/** A file of the drop and the slot it owns in the shared progress array. */
interface UploadSlot {
    entry: DroppedFile;
    index: number;
}

export function useFileDropUpload(options: FileDropUploadOptions) {
    /** A run is in flight (or still being collected). */
    let uploading = $state(false);
    let filesDone = $state(0);
    let filesTotal = $state(0);
    let bytesDone = $state(0);
    let bytesTotal = $state(0);
    let currentFile = $state('');

    let controller: AbortController | null = null;

    const percent = $derived(bytesTotal > 0 ? Math.min(100, Math.round((bytesDone / bytesTotal) * 100)) : 0);

    /** A file drag is hovering the list; the drop starts the run. */
    const dropZone = useFileDropZone((e) => {
        const dataTransfer = e.dataTransfer;
        if (!dataTransfer) return;
        if (!begin()) return;
        void collectDroppedFiles(dataTransfer).then(run).catch(reportUnexpected).finally(endRun);
    });
    const { handleDragEnter, handleDragOver, handleDragLeave, handleDrop } = dropZone;

    /**
     * The pointer-free path into the same run: files chosen by an `<input type="file">`
     * — a phone's file sheet or a keyboard, neither of which can start a file drag. A
     * picker reports bare names, so these land flat; a folder's tree is the drag's job.
     */
    function uploadFiles(files: File[]) {
        if (files.length === 0 || !begin()) return;
        void run(collectPickedFiles(files)).catch(reportUnexpected).finally(endRun);
    }

    /**
     * The `<input type="file">` answer: the same run a drop starts, from a control a
     * keyboard or a phone can reach. The input is cleared before the run — a value left
     * behind means picking the same file twice fires change once, and the second drop
     * looks like one the app ignored.
     */
    function handlePicked(e: Event) {
        const el = e.target as HTMLInputElement;
        const files = Array.from(el.files || []);
        el.value = '';
        uploadFiles(files);
    }

    /** One run at a time — a second drop or pick would race the same progress state. */
    function begin(): boolean {
        if (uploading) return false;
        uploading = true;
        return true;
    }

    function endRun() {
        uploading = false;
        dropZone.clearHover();
        currentFile = '';
        controller = null;
    }

    function reportUnexpected(e: unknown) {
        // Nothing here is expected to throw: entry reads are caught during collection
        // and transport failures come back as results. A throw is a bug, and a silent
        // one leaves the user looking at a listing that never moved.
        console.error('File upload failed:', e);
        dialogStore.showToast(t('explorer.shared.dropError', { error: String(e) }), 'error');
    }

    /** Stop the run: every in-flight request is aborted and nothing further starts. */
    function cancel() {
        controller?.abort();
    }

    function syncProgress(loaded: number[]) {
        let bytes = 0;
        for (const value of loaded) bytes += value;
        bytesDone = bytes;
    }

    /**
     * Upload `slots`, writing each file's byte count into the shared `loaded` array at
     * the slot's own index, so a retry pass reports into the same progress bar as the
     * run it belongs to. Outcomes come back keyed by that index.
     */
    async function uploadAll(slots: UploadSlot[], overwrite: boolean, loaded: number[]) {
        const signal = controller?.signal;
        const outcomes = new Map<number, UploadResult>();
        let next = 0;

        const worker = async () => {
            for (;;) {
                if (signal?.aborted) return;
                const position = next++;
                if (position >= slots.length) return;
                const { entry, index } = slots[position];
                currentFile = entry.relPath;
                const result = await uploadFsFile({
                    projectId: options.projectId(),
                    taskId: options.taskId?.(),
                    dir: options.dir(),
                    relPath: entry.relPath,
                    file: entry.file,
                    overwrite,
                    signal,
                    onProgress: (bytes) => {
                        loaded[index] = bytes;
                        syncProgress(loaded);
                    },
                });
                outcomes.set(index, result);
                if (result.status === 'ok') {
                    loaded[index] = entry.file.size;
                    filesDone++;
                }
                syncProgress(loaded);
            }
        };

        await Promise.all(Array.from({ length: Math.min(UPLOAD_CONCURRENCY, slots.length) }, () => worker()));
        return outcomes;
    }

    async function run(selection: DroppedSelection) {
        const notes: string[] = [];
        if (selection.tooBig.length) {
            notes.push(t('explorer.shared.dropTooBig', {
                count: selection.tooBig.length,
                max: Math.round(MAX_UPLOAD_BYTES / (1024 * 1024)),
            }));
        }
        if (selection.unreadable.length) {
            notes.push(t('explorer.shared.dropUnreadable', { count: selection.unreadable.length }));
        }
        if (selection.truncated) {
            notes.push(t('explorer.shared.dropTruncated', { max: MAX_DROP_FILES }));
        }
        if (selection.files.length === 0) {
            if (notes.length) dialogStore.showToast(notes.join(' · '), 'error');
            return;
        }

        controller = new AbortController();
        const slots: UploadSlot[] = selection.files.map((entry, index) => ({ entry, index }));
        const loaded = new Array<number>(slots.length).fill(0);
        filesDone = 0;
        filesTotal = slots.length;
        bytesTotal = slots.reduce((sum, slot) => sum + slot.entry.file.size, 0);
        bytesDone = 0;

        const outcomes = await uploadAll(slots, false, loaded);

        // Every clash is known now, so the user answers for the whole drop once.
        const conflicts = slots.filter((slot) => outcomes.get(slot.index)?.status === 'conflict');
        if (conflicts.length > 0 && !controller.signal.aborted) {
            const confirmed = await dialogStore.showConfirm({
                title: t('explorer.shared.dropOverwriteTitle'),
                message: t('explorer.shared.dropOverwriteMessage', { count: conflicts.length }),
                items: conflicts.slice(0, 8).map((slot) => slot.entry.relPath),
                confirmText: t('explorer.shared.dropOverwriteConfirm'),
                cancelText: t('explorer.shared.cancel'),
                variant: 'destructive',
            });
            if (!confirmed) {
                // Declining cancels the whole run: starting the rest would keep
                // writing after the user said stop.
                controller.abort();
                finish(outcomes, selection.files, notes, true);
                return;
            }
            const retried = await uploadAll(conflicts, true, loaded);
            for (const [index, result] of retried) outcomes.set(index, result);
        }
        finish(outcomes, selection.files, notes, controller.signal.aborted);
    }

    function finish(
        outcomes: Map<number, UploadResult>,
        files: DroppedFile[],
        notes: string[],
        cancelled: boolean,
    ) {
        const uploaded = files.filter((_, index) => outcomes.get(index)?.status === 'ok').map((entry) => entry.relPath);
        if (uploaded.length > 0) options.onUploaded(uploaded);

        const failed = [...outcomes.values()].filter((result) => result.status === 'error');
        // The outcome first, then the caveats. Suppressing the count when a caveat
        // existed left "1 file is over 20 MB" as the whole report of a run that had
        // also written three files.
        const messages: string[] = [];
        if (uploaded.length > 0 && !cancelled) {
            messages.push(t('explorer.shared.dropUploaded', { count: uploaded.length }));
        }
        messages.push(...notes);
        if (failed.length > 0) {
            messages.push(t('explorer.shared.dropFailed', { count: failed.length, error: failed[0].message || '' }));
        }
        let type: 'error' | 'success' = failed.length > 0 || notes.length > 0 ? 'error' : 'success';
        if (cancelled) {
            // The cancelled message carries the count, so the success line is not shown too.
            messages.push(t('explorer.shared.dropCancelled', { count: uploaded.length }));
            type = 'error';
        }
        if (messages.length > 0) dialogStore.showToast(messages.join(' · '), type);
    }

    return {
        get isOver() { return dropZone.isOver; },
        get uploading() { return uploading; },
        get filesDone() { return filesDone; },
        get filesTotal() { return filesTotal; },
        get bytesDone() { return bytesDone; },
        get bytesTotal() { return bytesTotal; },
        get currentFile() { return currentFile; },
        get percent() { return percent; },
        handleDragEnter,
        handleDragOver,
        handleDragLeave,
        handleDrop,
        uploadFiles,
        handlePicked,
        cancel,
    };
}

/**
 * What `useFileDropUpload` hands a listing, named so the components that render a drop
 * zone (FileDropProgress, FileUploadButton) can say what they take. Read through the
 * getters, never destructured: a destructured value is a snapshot of one render.
 */
export type FileDropUpload = ReturnType<typeof useFileDropUpload>;
