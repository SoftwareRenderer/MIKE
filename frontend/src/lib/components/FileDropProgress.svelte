<script lang="ts">
    import { t } from '$lib/i18n';
    import type { FileDropUpload } from '$lib/composables/use-file-drop-upload.svelte';

    /**
     * What a drop zone shows while files hover it and while they upload. Both file
     * browsers render it inside their own listing — the listing is the drop target, and
     * rendering it even when the folder is empty is what gives a drop somewhere to land.
     *
     * Its styles live in $lib/styles/file-drop.css as :global() rules rather than a
     * scoped copy per browser, which is what makes hosting them here possible: they match
     * on class alone, and the listing that imports the file is always an ancestor.
     */
    let { drop }: { drop: FileDropUpload } = $props();
</script>

{#if drop.uploading}
    <div class="drop-progress" role="status">
        <div class="drop-progress-row">
            <span class="drop-progress-label">{t('explorer.shared.dropUploading', { done: drop.filesDone, total: drop.filesTotal })}</span>
            <button class="drop-cancel-btn" onclick={drop.cancel}>{t('explorer.shared.dropCancelUpload')}</button>
        </div>
        <div class="drop-bar" role="progressbar" aria-valuemin={0} aria-valuemax={100} aria-valuenow={drop.percent}>
            <div class="drop-bar-fill" style="width: {drop.percent}%"></div>
        </div>
        {#if drop.currentFile}
            <span class="drop-progress-file">{drop.currentFile}</span>
        {/if}
    </div>
{:else if drop.isOver}
    <div class="drop-hint">{t('explorer.shared.dropHint')}</div>
{/if}
