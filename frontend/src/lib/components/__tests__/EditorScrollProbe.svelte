<script lang="ts">
    import FileEditor from '../FileEditor.svelte';

    /**
     * Test-only wrapper that owns the editor's scroll offset as its own state, so a test can hand the pane an
     * offset after it was built. Rerendering cannot do that, because it re-runs every effect in `FileEditor`.
     */
    let { projectId, projectPath, filePath, register }: {
        projectId: string;
        projectPath: string;
        filePath: string;
        register: (set: (top: number) => void) => void;
    } = $props();

    let top = $state(0);

    // svelte-ignore state_referenced_locally — the setter is meant to close over this instance's state.
    register((value) => { top = value; });
</script>

<FileEditor
    {projectId}
    {projectPath}
    {filePath}
    targetLine={0}
    initialScrollTop={top}
    onClose={() => {}}
/>
