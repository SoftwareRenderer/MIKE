<script lang="ts">
    import BaseModal from './BaseModal.svelte';
    import { t } from '$lib/i18n';
    import { uiInjector, INJECTION_POINTS } from '$lib/services/ui-injector.svelte';
    import { previewableBinary } from '$lib/utils/binary-preview';

    /**
     * The Preview a binary file gets when an installed extension declared it renders that file — the
     * sibling of "View image", for a format the core knows nothing about. This holds no bytes: the
     * frame it hosts asks for them through `fs.getBytes`, and it exists only while someone is looking.
     */
    let { project, task = '', path, displayPath = '', baseRef = '', onClose }: {
        project: string;
        /** Task whose worktree holds the file. */
        task?: string;
        /** Repository-relative path — the shape GET /api/fs/read?kind=bytes reads. */
        path: string;
        displayPath?: string;
        /** Set when there is an old side: the ref the diff compares against. */
        baseRef?: string;
        onClose: () => void;
    } = $props();

    const title = $derived(displayPath || path);

    // The mount context is the only thing that names a file to the byte capability.
    const context = $derived({
        component: 'BinaryPreviewModal',
        projectId: project,
        taskId: task,
        repoPath: path,
        filePath: displayPath || path,
        baseRef,
    });

    let anchorEl = $state<HTMLDivElement | null>(null);
    const claims = $derived(previewableBinary(path, uiInjector.getRegistrations(INJECTION_POINTS.BINARY_PREVIEW, context)));

    $effect(() => {
        const el = anchorEl;
        // Reading `context` here is what re-mounts the frame when the preview is pointed at another
        // file or version; without it the frame would keep the grants of the file that opened it.
        void context;
        if (!el) return;
        uiInjector.renderSlot(INJECTION_POINTS.BINARY_PREVIEW, context, el);
        return () => uiInjector.teardownSlot(INJECTION_POINTS.BINARY_PREVIEW, el);
    });
</script>

<BaseModal {title} width="min(1080px, 92vw)" bodyStyle="padding: 12px 16px;" {onClose}>
    {#if claims}
        <div class="binary-preview-body" bind:this={anchorEl}></div>
    {:else}
        <p class="binary-preview-none">{t('preview.noViewer')}</p>
    {/if}
</BaseModal>

<style>
    /* The frame sizes itself with ui.setSize/setFill; this is the room before it asks for any. */
    .binary-preview-body {
        min-height: 60vh;
        position: relative;
        width: 100%;
    }

    .binary-preview-none {
        margin: 0;
        padding: 24px 0;
        text-align: center;
        color: var(--color-text-tertiary);
        font-size: var(--text-sm);
    }
</style>
