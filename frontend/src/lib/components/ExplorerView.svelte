<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import { getGitLogFull, getGitBranches, checkoutBranch, getCommitDiffFiles } from '$lib/api';
    import { toHomeRelative, sandboxMountFromPath, parentDir } from '$lib/utils/path';
    import { projectTerminalHref, parseExplorerFile } from '$lib/utils/app-link';
    import { useIsMobile } from '$lib/composables/use-is-mobile.svelte';
    import { fromPersistedTabs, mergeRestoredTabs } from '$lib/utils/editor-tabs';
    import { ExplorerCore, type ExplorerProps } from '$lib/services/explorer-core.svelte';
    import { ExplorerLayout } from '$lib/services/explorer-layout.svelte';
    import { t } from '$lib/i18n';
    import BranchFileBrowser from './BranchFileBrowser.svelte';
    import ExplorerHeader from './ExplorerHeader.svelte';
    import ExplorerWorkspace from './ExplorerWorkspace.svelte';

    let { projectId, projectPath, currentBranch, initialFile, sandboxEnabled, onClose, fsEventQueue, onOpenTerminal, onUpdateFile, changesOpen, onChangesOpen }: ExplorerProps & {
        projectPath: string;
        currentBranch: string;
        initialFile: string;
    } = $props();

    const isMobileCtx = useIsMobile();

    // Sandbox mount path for this project
    let mountPath = $derived(sandboxEnabled ? sandboxMountFromPath(projectPath) : '');

    let selectedBranch = $state('');
    let branches = $state<string[]>([]);
    let loadingBranches = $state(true);
    let checkoutLoading = $state(false);
    let showDropdown = $state(false);
    let navigatedFolder = $state('');

    const core = new ExplorerCore({
        storageKey: () => projectId,
        // Project root and, when sandboxed, the mount the same directory appears under.
        pathRoots: () => (sandboxEnabled ? [projectPath, mountPath] : [projectPath]),
        isMobile: isMobileCtx,
        fsEvents: () => fsEventQueue,
        lineSelectionParent: () => null,
        updateFile: (path) => onUpdateFile(path),
        changesOpen: () => changesOpen,
        setChangesOpen: (open) => onChangesOpen(open),
        closeView: () => onClose(),
        fetchLog: (offset, limit) => getGitLogFull(projectId, { branch: selectedBranch, path: focusedFileRelPath(), limit, offset }),
        fetchCommitDiff: (baseHash, headHash) => getCommitDiffFiles(projectId, baseHash, headHash),
        consumeEscape: () => {
            if (!showDropdown) return false;
            showDropdown = false;
            return true;
        },
    });

    const layout = new ExplorerLayout(isMobileCtx);

    /** What the URL hands this explorer: a path inside it, and whether it names a folder to list. */
    let urlTarget = $derived.by(() => {
        const target = parseExplorerFile(initialFile);
        return core.isPathSafe(target.path) ? target : { path: '', isDir: false };
    });
    let urlFolder = $derived(urlTarget.isDir ? urlTarget.path : parentDir(urlTarget.path));
    /** The folder the listing shows: the URL's until the user navigates, then theirs. */
    let currentFolderPath = $derived(navigatedFolder || urlFolder || (sandboxEnabled ? mountPath : projectPath));

    // Sync selectedBranch when the parent updates currentBranch via WebSocket
    $effect(() => {
        if (currentBranch) selectedBranch = currentBranch;
    });

    function handleBranchSelect(branch: string) {
        if (branch !== selectedBranch) {
            handleCheckout(branch);
        }
    }

    async function loadBranches() {
        loadingBranches = true;
        try {
            const allBranches = await getGitBranches(projectId);
            branches = allBranches.filter(b => !b.includes('/'));
        } catch (e) {
            console.error('Failed to load branches:', e);
            branches = [];
        } finally {
            loadingBranches = false;
        }
    }

    async function handleCheckout(branch: string) {
        if (branch === selectedBranch || checkoutLoading) return;
        checkoutLoading = true;
        try {
            await checkoutBranch(projectId, branch);
            selectedBranch = branch;
            // Commits reload via the effect watching selectedBranch
        } catch (e) {
            console.error('Failed to checkout branch:', e);
        } finally {
            checkoutLoading = false;
        }
    }

    /** The log narrowed to the file being read: repo-relative, no leading slash. */
    function focusedFileRelPath(): string | undefined {
        const active = core.activeFilePath;
        if (!active) return undefined;
        let relPath: string | undefined;
        if (active.startsWith(projectPath)) {
            relPath = active.slice(projectPath.length);
        } else if (sandboxEnabled && mountPath && active.startsWith(mountPath)) {
            relPath = active.slice(mountPath.length);
        }
        return relPath?.startsWith('/') ? relPath.slice(1) : relPath;
    }

    // Reload history when focused file or branch changes. fetchLog reads both again when it runs;
    // this effect only decides when a reload is due.
    $effect(() => {
        const branch = selectedBranch;
        core.activeFilePath;
        if (branch) void core.loadCommits();
    });

    // Clear open files when project changes, then restore from IndexedDB. The project view keeps the
    // stored list as it stands — unlike the task view, its tabs are not filtered to one root — but
    // merges in a tab that arrived while the restore was in flight, which is what a window opened from
    // a file link brings: its own tab, ahead of the ones the other window left behind.
    $effect(() => {
        core.resetTabs(projectId);
        core.loadSavedTabs().then((saved) => {
            if (!saved) return;
            core.scrollTops = saved.scrollTops ?? {};
            core.fileListScroll = saved.fileListScroll ?? 0;
            if (saved.openFiles.length === 0) return;
            const openWas = core.activeFilePath;
            const restored = mergeRestoredTabs(core.openFiles, fromPersistedTabs(saved.openFiles));
            const active = openWas || saved.openFiles.find(f => f.path === saved.activeFilePath)?.path || '';
            core.applyTabs(restored, active, new Set(saved.pinnedFiles));
            if (!openWas) core.updateFile(active || null);
        }).catch(() => {});
    });

    // Sync selected file from URL on mount and when initialFile updates. A folder link opens no tab:
    // it only seeds the listing, which currentFolderPath does.
    $effect(() => {
        const file = urlTarget.path;
        if (file && !urlTarget.isDir && core.openFiles.length === 0) {
            core.openFiles = [{ path: file, targetLine: 0, reveal: 0 }];
            core.activeFilePath = file;
        }
    });

    // The browsed folder belongs to a project, and this component outlives a project switch.
    // Without the reset, /api/fs (which resolves the project from the path) is asked for the old
    // project's folder: refused or wrong, and the panel sits on an error nothing retries.
    let seededForProject = $state<string | null>(null);
    $effect(() => {
        if (projectPath && seededForProject !== projectPath) {
            seededForProject = projectPath;
            navigatedFolder = '';
        }
    });

    // File-list scroll: capture it on every scroll, restore it once the listing has something to scroll.
    // svelte-ignore non_reactive_update — only the first listing after mount is restored
    let fileListScrollArmed = true;
    $effect(() => {
        const el = core.fileBrowserEl;
        if (!el) return;
        el.addEventListener('scroll', handleFileListScrollEvent, true);
        if (!fileListScrollArmed) {
            return () => el.removeEventListener('scroll', handleFileListScrollEvent, true);
        }
        fileListScrollArmed = false;
        let attempts = 0;
        const timer = setInterval(() => {
            attempts++;
            const list = el.querySelector<HTMLElement>('.file-browser-list');
            if (list && core.fileListScroll > 0 && list.scrollHeight > list.clientHeight) {
                clearInterval(timer);
                list.scrollTop = Math.min(core.fileListScroll, list.scrollHeight - list.clientHeight);
            } else if (attempts >= 20) {
                clearInterval(timer);
            }
        }, 100);
        return () => {
            clearInterval(timer);
            el.removeEventListener('scroll', handleFileListScrollEvent, true);
        };
    });

    function handleFileListScrollEvent() {
        const list = core.fileBrowserEl?.querySelector<HTMLElement>('.file-browser-list');
        if (!list) return;
        core.fileListScroll = list.scrollTop;
        core.queueTabSave();
    }

    onMount(() => {
        void core.loadCommits();
        void loadBranches();
        // If a file is already open, switch to editor tab
        if (core.openFiles.length > 0) {
            core.activeTab = 'editor';
        }
        window.addEventListener('keydown', core.handleKeydown);
        window.addEventListener('open-file', handleOpenFile);
        core.mount();
    });

    // open-file dispatch (go-to-definition / search results)
    function handleOpenFile(e: Event) {
        const { path, line, taskId } = (e as CustomEvent<{ path: string; line?: number; taskId?: string }>).detail;
        if (taskId) return;
        core.handleFileSelected(path, line);
    }

    onDestroy(() => {
        window.removeEventListener('keydown', core.handleKeydown);
        window.removeEventListener('open-file', handleOpenFile);
        core.unmount();
    });
</script>

<div class="explorer-view" class:fullscreen={core.editorFullscreen && core.activeFilePath} role="dialog" aria-label={t('explorer.view.aria')}>
    <!-- Header bar (replaces top-bar when explorer is open) -->
    <ExplorerHeader
        pathLabel={toHomeRelative(projectPath)}
        closeAria={t('explorer.view.closeAria')}
        branch={selectedBranch}
        branches={branches}
        branchesLoading={loadingBranches}
        checkoutLoading={checkoutLoading}
        bind:dropdownOpen={showDropdown}
        onSelectBranch={handleBranchSelect}
        changesOpen={changesOpen}
        onChangesOpen={onChangesOpen}
        showReview
        onCloseRequest={() => { core.editorFullscreen = false; core.closeCommitDiff(); onClose(); }}
        onOpenTerminal={onOpenTerminal}
        terminalHref={projectTerminalHref(projectId)}
    />

    <ExplorerWorkspace
        {core}
        {layout}
        isMobile={isMobileCtx.isMobile}
        branch={selectedBranch}
        projectId={projectId}
        projectPath={projectPath}
        folderPath={currentFolderPath}
        changesOpen={changesOpen}
        onChangesOpen={onChangesOpen}
        onReviewUpdated={() => { void core.loadCommits(); void loadBranches(); }}
        scrollTops={core.scrollTops}
        onScrollTop={core.handleEditorScrollTop}
    >
        {#snippet browser()}
            <BranchFileBrowser
                projectId={projectId}
                projectPath={projectPath}
                initialPath={currentFolderPath}
                sandboxEnabled={sandboxEnabled}
                unsavedFiles={[...core.unsavedFiles]}
                pendingFsEvent={core.pendingFsEvent}
                onFileSelected={core.handleFileSelected}
                onDeleted={core.handleFilesDeleted}
                onPathChange={(p) => navigatedFolder = p}
                onToggleFind={core.toggleFind}
            />
        {/snippet}
    </ExplorerWorkspace>
</div>

<style>
    @import '$lib/styles/explorer.css';
</style>
