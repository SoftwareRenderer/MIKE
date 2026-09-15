<script lang="ts">
    import FileBrowserView from '$lib/components/FileBrowserView.svelte';
    import { t } from '$lib/i18n';
    import { taskRelativeFsPath, type FsEvent } from '$lib/utils/fs-event';

    let { projectId, projectName, taskId, taskPath, sandboxEnabled, unsavedFiles, pendingFsEvent, onFileSelected, onPathChange, onToggleFind }: {
        projectId: string;
        projectName: string;
        taskId: string;
        taskPath: string;
        sandboxEnabled: boolean;
        unsavedFiles: string[];
        pendingFsEvent: FsEvent | null;
        onFileSelected: (path: string, line?: number, pin?: boolean) => void;
        onPathChange: (path: string) => void;
        onToggleFind?: () => void;
    } = $props();

    // Sandbox mount path: /home/sandbox/<projectName>
    let mountPath = $derived(sandboxEnabled ? '/home/sandbox/' + projectName : '');
    // Everything listed, written and dropped lands under this: the mount when sandboxed, the
    // task's own worktree otherwise.
    let rootPath = $derived(sandboxEnabled ? mountPath : taskPath);

    // Events reach us from two watchers with two roots: WorktreeWatcher speaks the task worktree,
    // ProjectWatcher the project root. A sibling task's change is not this listing's business.
    function mapFsEvent(event: FsEvent): string | null {
        return taskRelativeFsPath(event, projectId, taskId);
    }

    // A task explorer tab is never pinned, so a double-click is just a click.
    function openFile(path: string) {
        onFileSelected(path);
    }
</script>

<FileBrowserView
    {projectId}
    {taskId}
    {rootPath}
    navMode="breadcrumb"
    showDotFiles={false}
    retryMissingDir
    listingClass="file-list"
    rowClass="file-entry"
    {unsavedFiles}
    {pendingFsEvent}
    {mapFsEvent}
    emptyMessage={t('explorer.taskFiles.empty')}
    pendingMessage={t('explorer.shared.loading')}
    onFileSelected={openFile}
    {onPathChange}
    {onToggleFind}
/>
