import { dialogStore } from '$lib/stores';
import { t } from '$lib/i18n';
import type { DraftAttachment } from '$lib/storage';
import { dragCarriesFiles } from '$lib/utils/file-drop';
import {
    MAX_IMAGES,
    describeRejectedImages,
    readImageAttachments,
    readTextFile,
    sortTransferFiles,
    takeTransfer,
    type TransferBuckets,
} from '$lib/utils/attachments';

/**
 * The paste, drop and picker handling of one message input: a raster a provider can be shown
 * becomes an attachment, a text document is inlined where the cursor is, and anything else is
 * written into the task's own environment and named by path.
 */
export interface ImageAttachmentsOptions {
    getTextarea: () => HTMLTextAreaElement | null | undefined;
    getValue: () => string;
    setValue: (value: string) => void;
    held: () => number;
    addImages: (accepted: DraftAttachment[]) => void;
    /**
     * Read as a closure, never as a value captured at construction: `projectId`/`taskId` are
     * props, and a surface can gain its task (or lose it) after this is built.
     */
    uploadFile?: (file: File) => Promise<string>;
    /** False where there is no task environment to write into (the create-task modal). */
    hasTask?: () => boolean;
    onChanged?: () => void;
}

export function useImageAttachments(options: ImageAttachmentsOptions) {
    let isOver = $state(false);
    let dragDepth = 0;

    function insert(text: string) {
        const value = options.getValue();
        const textarea = options.getTextarea();
        const start = textarea?.selectionStart ?? value.length;
        const end = textarea?.selectionEnd ?? value.length;
        options.setValue(value.slice(0, start) + text + value.slice(end));
        const caret = start + text.length;
        try {
            textarea?.setSelectionRange(caret, caret);
        } catch {
            // A field that lost focus has no selection to restore; the text still landed.
        }
    }

    function report(message: string) {
        dialogStore.showToast(message, 'error');
    }

    async function takeImages(files: File[]) {
        const picked = await readImageAttachments(files, MAX_IMAGES - options.held());
        if (picked.accepted.length > 0) options.addImages(picked.accepted);
        const problem = describeRejectedImages(picked);
        if (problem) report(problem);
    }

    async function takeTexts(files: File[]) {
        for (const file of files) {
            try {
                const { content, droppedBytes } = await readTextFile(file);
                insert(`\n\n${t('attachments.inlinedFile', { name: file.name })}\n\`\`\`\n${content}\n\`\`\`\n`);
                if (droppedBytes > 0) {
                    insert(`\n${t('attachments.truncatedText', { name: file.name, kb: Math.round(droppedBytes / 1024) })}\n`);
                }
            } catch (err) {
                console.error('Failed to read text document:', err);
                report(t('attachments.couldNotRead', { name: file.name }));
            }
        }
    }

    async function takeFiles(files: File[]) {
        if (!options.uploadFile || !options.hasTask?.()) {
            report(t('attachments.fileNeedsTask', { count: files.length }));
            return;
        }
        for (const file of files) {
            try {
                insert(`\n\n${t('attachments.uploadedFile', { name: file.name, path: await options.uploadFile(file) })}\n`);
            } catch (err) {
                console.error('Failed to upload file:', err);
                report(err instanceof Error && err.message ? err.message : t('attachments.uploadFailed', { name: file.name }));
            }
        }
    }

    async function intake(buckets: TransferBuckets) {
        if (buckets.images.length > 0) await takeImages(buckets.images);
        if (buckets.texts.length > 0) await takeTexts(buckets.texts);
        if (buckets.files.length > 0) await takeFiles(buckets.files);
        options.onChanged?.();
    }

    function handlePaste(e: ClipboardEvent) {
        const taken = takeTransfer(e);
        if (!taken) return;
        void intake(taken);
    }

    function handlePicked(e: Event) {
        const input = e.target as HTMLInputElement;
        const files = Array.from(input.files ?? []);
        // A value left behind means picking the same file twice fires change once.
        input.value = '';
        if (files.length === 0) return;
        void intake(sortTransferFiles(files));
    }

    function handleDragEnter(e: DragEvent) {
        if (!dragCarriesFiles(e.dataTransfer)) return;
        e.preventDefault();
        dragDepth++;
        isOver = true;
    }

    function handleDragOver(e: DragEvent) {
        if (!dragCarriesFiles(e.dataTransfer)) return;
        // Without this the browser refuses the drop and opens the file in the tab.
        e.preventDefault();
        if (e.dataTransfer) e.dataTransfer.dropEffect = 'copy';
        isOver = true;
    }

    function handleDragLeave(e: DragEvent) {
        if (!dragCarriesFiles(e.dataTransfer)) return;
        dragDepth = Math.max(0, dragDepth - 1);
        if (dragDepth === 0) isOver = false;
    }

    function handleDrop(e: DragEvent) {
        if (!dragCarriesFiles(e.dataTransfer)) return;
        e.preventDefault();
        dragDepth = 0;
        isOver = false;
        const taken = takeTransfer(e);
        if (!taken) return;
        void intake(taken);
    }

    return {
        get isOver() {
            return isOver;
        },
        insert,
        handlePaste,
        handlePicked,
        handleDragEnter,
        handleDragOver,
        handleDragLeave,
        handleDrop,
    };
}
