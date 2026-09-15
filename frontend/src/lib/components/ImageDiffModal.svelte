<script lang="ts">
    import BaseModal from './BaseModal.svelte';
    import { rawFileURL } from '$lib/api';
    import { t } from '$lib/i18n';

    /**
     * One binary file's picture(s) from a diff: one pane when the change created the file, two when it
     * changed one. Each `<img>` loads from the server itself.
     */
    let { project, task = '', path, displayPath = '', oldPath = '', oldRef = '', newRef = '', onClose }: {
        project: string;
        /** Task whose worktree holds the file. */
        task?: string;
        /** Repository-relative path — the shape GET /api/fs/raw reads. */
        path: string;
        displayPath?: string;
        /** Set only when there is an old side: the ref, and the path it is filed under (a rename). */
        oldRef?: string;
        oldPath?: string;
        /** Ref the new side is read from, when that is not the working tree. */
        newRef?: string;
        onClose: () => void;
    } = $props();

    type Pane = 'old' | 'new';
    let state = $state<Record<Pane, 'loading' | 'loaded' | 'error'>>({ old: 'loading', new: 'loading' });

    const compare = $derived(!!oldRef);
    const oldURL = $derived(compare ? rawFileURL({ project, task, path: oldPath || path, ref: oldRef }) : '');
    const newURL = $derived(rawFileURL({ project, task, path, ref: newRef }));
    const title = $derived(displayPath || path);

    function mark(which: Pane, loaded: boolean) {
        state[which] = loaded ? 'loaded' : 'error';
    }
</script>

{#snippet pane(which: Pane, url: string, label: string | null)}
    <div class="image-pane">
        {#if label}
            <div class="image-pane-label">{label}</div>
        {/if}
        <div class="image-pane-body">
            {#if state[which] === 'loading'}
                <div class="image-pane-status">
                    <span class="image-spinner" aria-hidden="true"></span>
                    <span>{t('diff.shared.loading')}</span>
                </div>
            {:else if state[which] === 'error'}
                <div class="image-pane-status">{t('diff.imageUnavailable')}</div>
            {/if}
            <img
                src={url}
                alt={title}
                class="image-diff-img"
                class:hidden={state[which] !== 'loaded'}
                onload={() => mark(which, true)}
                onerror={() => mark(which, false)}
            />
        </div>
    </div>
{/snippet}

<BaseModal {title} width="min(1180px, 92vw)" bodyStyle="padding: 12px 16px; display: flex; gap: 12px;" {onClose}>
    {#if compare}
        {@render pane('old', oldURL, t('diff.oldSide'))}
        {@render pane('new', newURL, t('diff.newSide'))}
    {:else}
        {@render pane('new', newURL, null)}
    {/if}
</BaseModal>

<style>
    .image-pane {
        flex: 1 1 0;
        min-width: 0;
        display: flex;
        flex-direction: column;
        gap: 6px;
    }
    .image-pane-label {
        font-size: 0.6875rem;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.04em;
        color: var(--color-text-tertiary);
        flex-shrink: 0;
    }
    .image-pane-body {
        flex: 1;
        min-height: 0;
        display: flex;
        align-items: center;
        justify-content: center;
        overflow: hidden;
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        /* Transparency needs a background that is neither the app's light nor its dark. */
        background-color: #6a6a6a;
        background-image: linear-gradient(45deg, #5a5a5a 25%, transparent 25%), linear-gradient(-45deg, #5a5a5a 25%, transparent 25%), linear-gradient(45deg, transparent 75%, #5a5a5a 75%), linear-gradient(-45deg, transparent 75%, #5a5a5a 75%);
        background-size: 16px 16px;
        background-position: 0 0, 0 8px, 8px -8px, -8px 0;
    }
    .image-diff-img {
        max-width: 100%;
        max-height: min(72vh, 900px);
        object-fit: contain;
        display: block;
    }
    .image-diff-img.hidden {
        display: none;
    }
    .image-pane-status {
        display: flex;
        align-items: center;
        gap: 8px;
        color: rgba(255, 255, 255, 0.85);
        font-size: 0.75rem;
    }
    .image-spinner {
        width: 14px;
        height: 14px;
        border: 2px solid rgba(255, 255, 255, 0.3);
        border-top-color: rgba(255, 255, 255, 0.9);
        border-radius: 50%;
        animation: image-spin 0.8s linear infinite;
    }
    @keyframes image-spin {
        to {
            transform: rotate(360deg);
        }
    }
</style>
