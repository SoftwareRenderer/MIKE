<script lang="ts">
    import FileBrowserView from '$lib/components/FileBrowserView.svelte';
    import { t } from '$lib/i18n';
    import { sandboxMountPath } from '$lib/utils/path';
    import { taskRelativeFsPath, type FsEvent } from '$lib/utils/fs-event';

    let { projectId, projectName, taskId, taskPath, initialPath = '', sandboxEnabled, unsavedFiles, pendingFsEvent, onFileSelected, onPathChange, onToggleFind, onDeleted }: {
        projectId: string;
        projectName: string;
        taskId: string;
        taskPath: string;
        /** Where the first listing opens — the folder a deep link named, when there was one. */
        initialPath?: string;
        sandboxEnabled: boolean;
        unsavedFiles: string[];
        pendingFsEvent: FsEvent | null;
        onFileSelected: (path: string, line?: number, pin?: boolean) => void;
        onPathChange: (path: string) => void;
        onToggleFind?: () => void;
        onDeleted?: (paths: string[]) => void;
    } = $props();

    // Sandbox mount path: /home/sandbox/<projectName>
    let mountPath = $derived(sandboxEnabled ? sandboxMountPath(projectName) : '');
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
    {initialPath}
    navMode="breadcrumb"
    showDotFiles={false}
    retryMissingDir
    listingClass="file-list"
    rowClass="file-entry"
    {unsavedFiles}
    {pendingFsEvent}
    {mapFsEvent}
    {onDeleted}
    emptyMessage={t('explorer.taskFiles.empty')}
    pendingMessage={t('explorer.shared.loading')}
    onFileSelected={openFile}
    {onPathChange}
    {onToggleFind}
/>
