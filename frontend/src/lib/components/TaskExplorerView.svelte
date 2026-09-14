<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import { getTaskGitLog, getTaskCommitDiffFiles, initTask } from '$lib/api';
    import { toHomeRelative, toExplorerPath } from '$lib/utils/path';
    import { useIsMobile } from '$lib/composables/use-is-mobile.svelte';
    import { fromPersistedTabs, mergeRestoredTabs } from '$lib/utils/editor-tabs';
    import { ExplorerCore, type ExplorerProps } from '$lib/services/explorer-core.svelte';
    import { ExplorerLayout } from '$lib/services/explorer-layout.svelte';
    import { t } from '$lib/i18n';
    import ExplorerHeader from './ExplorerHeader.svelte';
    import ExplorerWorkspace from './ExplorerWorkspace.svelte';
    import TaskFileBrowser from './TaskFileBrowser.svelte';

    let { projectId, projectName, taskId, taskPath, taskBranch, taskStatus, sandboxEnabled, onClose, fsEventQueue, onOpenTerminal, onUpdateFile, changesOpen, onChangesOpen }: ExplorerProps & {
        projectName: string;
        taskId: string;
        taskPath: string;
        taskBranch: string;
        taskStatus: string;
    } = $props();

    const isMobileCtx = useIsMobile();

    // Sandbox mount path for this task: /home/sandbox/<projectName>
    let mountPath = $derived(sandboxEnabled ? '/home/sandbox/' + projectName : '');

    let selectedBranch = $state('');
    let currentFolderPath = $state('');

    const core = new ExplorerCore({
        storageKey: () => projectId,
        // Task worktree and, when sandboxed, the mount the same directory appears under.
        pathRoots: () => (sandboxEnabled ? [taskPath, mountPath] : [taskPath]),
        isMobile: isMobileCtx,
        fsEvents: () => fsEventQueue,
        lineSelectionParent: () => taskId,
        updateFile: (path) => onUpdateFile(path),
        changesOpen: () => changesOpen,
        setChangesOpen: (open) => onChangesOpen(open),
        closeView: () => onClose(),
        fetchLog: (offset, limit) => getTaskGitLog(projectId, taskId, { limit, offset }),
        fetchCommitDiff: (baseHash, headHash) => getTaskCommitDiffFiles(projectId, taskId, baseHash, headHash),
        prepareFileOpen: ensureWorktree,
    });

    const layout = new ExplorerLayout(isMobileCtx);

    /** An unassigned task has no worktree yet, and there is nothing to read until it gets one. */
    async function ensureWorktree(): Promise<boolean> {
        if (taskBranch) return true;
        try {
            await initTask(projectId, taskId);
            return true;
        } catch (e) {
            console.error('[task-explorer] Failed to initialize task:', e);
            return false;
        }
    }

    // Status color for branch badge
    let statusColor = $derived(
        taskStatus === 'backlog' ? 'var(--color-column-backlog)' :
        taskStatus === 'in_progress' ? 'var(--color-column-in-progress)' :
        taskStatus === 'review' ? 'var(--color-column-review)' :
        taskStatus === 'done' ? 'var(--color-column-done)' :
        'var(--color-text-secondary)'
    );

    // Sync selectedBranch when parent updates
    $effect(() => {
        if (taskBranch) selectedBranch = taskBranch;
    });

    // Clear open files when task changes, then restore from IndexedDB. A jump can be what opens the
    // explorer, so the tabs a restore races with are merged in, and only the ones inside this task's
    // worktree are worth having.
    $effect(() => {
        const _ = taskId;
        core.resetTabs();
        core.loadSavedTabs().then((saved) => {
            if (!saved || saved.openFiles.length === 0) return;
            const openWas = core.activeFilePath;
            const restored = mergeRestoredTabs(core.openFiles, fromPersistedTabs(saved.openFiles.filter(f => core.isPathSafe(f.path))));
            const active = openWas || saved.openFiles.find(f => f.path === saved.activeFilePath && core.isPathSafe(f.path))?.path || '';
            core.applyTabs(restored, active, new Set(saved.pinnedFiles.filter(p => restored.some(f => f.path === p))));
            if (!openWas) core.updateFile(active || null);
            core.scrollTops = { ...saved.scrollTops, ...core.scrollTops };
            core.fileListScroll = core.fileListScroll || (saved.fileListScroll ?? 0);
        }).catch(() => {});
    });

    $effect(() => {
        if (!currentFolderPath) {
            currentFolderPath = sandboxEnabled ? mountPath : taskPath;
        }
    });

    onMount(() => {
        window.addEventListener('keydown', core.handleKeydown);
        window.addEventListener('open-file', handleOpenFile);
        core.mount();
        void (async () => {
            await ensureWorktree();
            await core.loadCommits();
            if (core.openFiles.length > 0) {
                core.activeTab = 'editor';
            }
        })();
    });

    // open-file dispatch (go-to-definition / search results, and a tool call's file)
    function handleOpenFile(e: Event) {
        const detail = (e as CustomEvent<{ path: string; line?: number; taskId?: string }>).detail;
        if (detail.taskId && detail.taskId !== taskId) return;
        core.handleFileSelected(toExplorerPath(detail.path, sandboxEnabled ? mountPath : taskPath), detail.line);
    }

    onDestroy(() => {
        window.removeEventListener('keydown', core.handleKeydown);
        window.removeEventListener('open-file', handleOpenFile);
        core.unmount();
    });
</script>

<div class="explorer-view" class:fullscreen={core.editorFullscreen && core.activeFilePath} role="dialog" aria-label={t('explorer.taskView.aria')}>
    <!-- Header bar -->
    <ExplorerHeader
        pathLabel={toHomeRelative(taskPath)}
        pathClamped
        closeAria={t('explorer.taskView.closeAria')}
        branch={selectedBranch}
        statusColor={statusColor}
        changesOpen={changesOpen}
        onChangesOpen={onChangesOpen}
        onCloseRequest={() => { core.editorFullscreen = false; core.closeCommitDiff(); onClose(); }}
        onOpenTerminal={onOpenTerminal}
    />

    <ExplorerWorkspace
        {core}
        {layout}
        isMobile={isMobileCtx.isMobile}
        branch={selectedBranch}
        projectId={projectId}
        projectPath={taskPath}
        taskId={taskId}
        folderPath={currentFolderPath}
        changesOpen={changesOpen}
        onChangesOpen={onChangesOpen}
    >
        {#snippet browser()}
            <TaskFileBrowser
                projectId={projectId}
                projectName={projectName}
                taskId={taskId}
                taskPath={taskPath}
                sandboxEnabled={sandboxEnabled}
                unsavedFiles={[...core.unsavedFiles]}
                pendingFsEvent={core.pendingFsEvent}
                onFileSelected={core.handleFileSelected}
                onPathChange={(p) => currentFolderPath = p}
                onToggleFind={core.toggleFind}
            />
        {/snippet}
    </ExplorerWorkspace>
</div>

<style>
    @import '$lib/styles/shared.css';
    @import '$lib/styles/explorer.css';
</style>
