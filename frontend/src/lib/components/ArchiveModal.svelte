<script lang="ts">
    import BaseModal from './BaseModal.svelte';
    import ArchiveViewer from './ArchiveViewer.svelte';
    
    /**
     * One archive's member list, framed over the diff panel that named the file: the panel knows which
     * commit to read, and the listing knows how to read one.
     */
    let { project, task = '', path, displayPath, ref = '', onClose }: {
        project: string;
        /** Task whose worktree holds the file; empty reads the project's own checkout. */
        task?: string;
        /** Repository-relative — the spelling the route reads. */
        path: string;
        /** What the row is called where it was clicked, rename included. */
        displayPath: string;
        /** The side of the comparison to read; empty is the file on disk. */
        ref?: string;
        onClose: () => void;
    } = $props();
</script>

<BaseModal
    title={displayPath}
    width="min(880px, 92vw)"
    bodyStyle="padding: 0; display: flex; flex-direction: column; height: min(70vh, 640px); min-height: 240px;"
    {onClose}
>
    <ArchiveViewer projectId={project} taskId={task} {path} {ref} />
</BaseModal>
