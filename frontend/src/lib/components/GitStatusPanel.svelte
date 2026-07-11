<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import type { DiffFile } from '$lib/types/diff';
    import { parseUnifiedDiff, truncatePath } from '$lib/utils/diff-parser';
    import { getGitStatus, getProjectDiff } from '$lib/api/git';
    import { loadAppSetting, saveAppSetting } from '$lib/storage';
    import DiffViewer from './DiffViewer.svelte';
    import ResizeHandle from './ResizeHandle.svelte';

    let { projectId, onClose, isMobile }: {
        projectId: string;
        onClose: () => void;
        isMobile: boolean;
    } = $props();

    // Parsed status entries for the sidebar file list
    interface StatusEntry {
        path: string;
        status: string; // M, A, D, ?, R, U, C
        staged: boolean;
        origPath?: string;
    }

    let stagedEntries = $state<StatusEntry[]>([]);
    let unstagedEntries = $state<StatusEntry[]>([]);

    // Diff data for DiffViewer
    let stagedDiffFiles = $state<DiffFile[]>([]);
    let unstagedDiffFiles = $state<DiffFile[]>([]);

    let loading = $state(true);
    let selectedPath = $state('');

    // Resizable sidebar width — persisted to IndexedDB
    const DEFAULT_SIDEBAR_WIDTH = 240;
    const MIN_SIDEBAR = 160;
    const MAX_SIDEBAR = 500;
    let sidebarWidth = $state(DEFAULT_SIDEBAR_WIDTH);

    loadAppSetting('kanban:changes:sidebarWidth', DEFAULT_SIDEBAR_WIDTH).then(v => {
        const val = Number.parseInt(String(v), 10);
        if (!isNaN(val) && val >= MIN_SIDEBAR && val <= MAX_SIDEBAR) {
            sidebarWidth = val;
        }
    }).catch(() => {});

    function handleDragStart(_e: MouseEvent | TouchEvent, startX: number) {
        dragStartX = startX;
        dragStartWidth = sidebarWidth;
    }
    let dragStartX = 0;
    let dragStartWidth = 0;
    function handleDragMove(pointerX: number) {
        const delta = pointerX - dragStartX;
        sidebarWidth = Math.max(MIN_SIDEBAR, Math.min(MAX_SIDEBAR, dragStartWidth + delta));
    }
    function handleDragEnd() {
        saveAppSetting('kanban:changes:sidebarWidth', sidebarWidth).catch(() => {});
    }

    // Collapsed sections in sidebar
    let collapsedSections = $state(new Set<string>());

    let hasContent = $derived(
        stagedDiffFiles.length > 0 ||
        unstagedDiffFiles.length > 0 ||
        stagedEntries.length > 0 ||
        unstagedEntries.length > 0
    );

    // Sort helpers
    function sortEntries(entries: StatusEntry[]): StatusEntry[] {
        return [...entries].sort((a, b) => {
            const aHasDir = a.path.includes('/');
            const bHasDir = b.path.includes('/');
            if (aHasDir && !bHasDir) return -1;
            if (!aHasDir && bHasDir) return 1;
            return a.path.localeCompare(b.path);
        });
    }

    function sortFiles(files: DiffFile[]): DiffFile[] {
        return [...files].sort((a, b) => {
            const aHasDir = a.path.includes('/');
            const bHasDir = b.path.includes('/');
            if (aHasDir && !bHasDir) return -1;
            if (!aHasDir && bHasDir) return 1;
            return a.path.localeCompare(b.path);
        });
    }

    let sortedStagedEntries = $derived(sortEntries(stagedEntries));
    let sortedUnstagedEntries = $derived(sortEntries(unstagedEntries));
    let sortedStagedFiles = $derived(sortFiles(stagedDiffFiles));
    let sortedUnstagedFiles = $derived(sortFiles(unstagedDiffFiles));

    async function loadData() {
        loading = true;
        try {
            // Fetch git status for sidebar
            const { status } = await getGitStatus(projectId);
            const staged: StatusEntry[] = [];
            const unstaged: StatusEntry[] = [];

            for (const line of status.split('\n').filter(l => l.trim())) {
                const indexChar = line[0];
                const workChar = line[1];
                const pathPart = line.substring(3).trim();

                const arrowIdx = pathPart.indexOf(' -> ');
                let filePath: string, origPath: string | undefined;
                if (arrowIdx >= 0) {
                    origPath = pathPart.substring(0, arrowIdx);
                    filePath = pathPart.substring(arrowIdx + 4);
                } else {
                    filePath = pathPart;
                }

                // Index changes (staged)
                if (indexChar !== ' ' && indexChar !== '?') {
                    staged.push({ path: filePath, status: indexChar, staged: true, origPath });
                }
                // Working tree changes (unstaged)
                if (workChar !== ' ' && workChar !== '?' && workChar !== indexChar) {
                    const existing = staged.find(s => s.path === filePath);
                    if (!existing) {
                        unstaged.push({ path: filePath, status: workChar, staged: false, origPath });
                    }
                } else if (workChar === '?' && indexChar === ' ') {
                    unstaged.push({ path: filePath, status: '?', staged: false });
                }
            }

            stagedEntries = staged;
            unstagedEntries = unstaged;

            // Fetch diffs for DiffViewer
            const [stagedDiff, unstagedDiff] = await Promise.all([
                getProjectDiff(projectId, { cached: true }).catch(() => ''),
                getProjectDiff(projectId).catch(() => ''),
            ]);

            stagedDiffFiles = parseUnifiedDiff(stagedDiff);
            unstagedDiffFiles = parseUnifiedDiff(unstagedDiff);
        } catch (e) {
            console.error('Failed to load git status:', e);
        } finally {
            loading = false;
        }
    }

    function toggleSection(id: string) {
        const next = new Set(collapsedSections);
        if (next.has(id)) next.delete(id);
        else next.add(id);
        collapsedSections = next;
    }

    function getStatusBadge(entry: StatusEntry): { letter: string; color: string } {
        switch (entry.status) {
            case 'M': return { letter: 'M', color: '#569cd6' };
            case 'A': return { letter: 'A', color: '#3fb950' };
            case 'D': return { letter: 'D', color: '#f85149' };
            case '?': return { letter: '??', color: '#8b949e' };
            case 'R': return { letter: 'R', color: '#d29922' };
            case 'U': return { letter: 'U', color: '#d29922' };
            case 'C': return { letter: 'C', color: '#58a6ff' };
            default: return { letter: entry.status, color: '#8b949e' };
        }
    }

    /** Split path into { folder, name } — folder is the parent dirs, name is the basename. */
    function splitPath(path: string): { folder: string; name: string } {
        const idx = path.lastIndexOf('/');
        if (idx < 0) return { folder: '', name: path };
        return { folder: path.substring(0, idx), name: path.substring(idx + 1) };
    }

    function getStatusLabel(char: string): string {
        switch (char) {
            case 'M': return 'Modified';
            case 'A': return 'Added';
            case 'D': return 'Deleted';
            case '?': return 'Untracked';
            case 'R': return 'Renamed';
            case 'U': return 'Unmerged';
            case 'C': return 'Copied';
            default: return char;
        }
    }



    function handleKeydown(e: KeyboardEvent) {
        if (e.key === 'Escape') {
            onClose();
        }
    }

    onMount(() => {
        loadData();
    });
</script>

<div class="git-status-panel">
    <!-- Header -->
    <div class="panel-header">
        <span class="panel-title">Changes</span>
        <button class="panel-icon-btn" onclick={loadData} title="Refresh" aria-label="Refresh">
            <svg width="12" height="12" viewBox="0 0 16 16" fill="none">
                <path d="M2 8a6 6 0 0 1 10.47-4M14 8a6 6 0 0 1-10.47 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
                <path d="M12 1v3h-3M4 15v-3h3" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
            </svg>
        </button>
    </div>

    {#if loading}
        <div class="panel-loading">
            <span class="spinner"></span>
            <span>Loading changes...</span>
        </div>
    {:else if !hasContent}
        <div class="panel-empty">
            <svg width="28" height="28" viewBox="0 0 16 16" fill="none">
                <path d="M3 2h6l4 4v8a1 1 0 0 1-1 1H3a1 1 0 0 1-1-1V3a1 1 0 0 1 1-1z" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round" opacity="0.3"/>
                <path d="M9 2v4h4" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round" opacity="0.3"/>
            </svg>
            <p>No changes since last commit</p>
        </div>
    {:else}
        <div class="panel-body">
            <!-- Left: Changes file list sidebar -->
            <div class="changes-sidebar" class:hidden={isMobile} style="width: {sidebarWidth}px">
                <div class="changes-scroll">
                    <!-- Staged entries -->
                    {#if sortedStagedEntries.length > 0}
                    <div class="changes-section">
                        <button class="changes-section-header" type="button" onclick={() => toggleSection('staged-files')} aria-expanded={!collapsedSections.has('staged-files')}>
                            <svg class="changes-chevron" class:expanded={!collapsedSections.has('staged-files')} width="10" height="10" viewBox="0 0 16 16" fill="none">
                                <path d="M6 4l4 4-4 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                            </svg>
                            <span>Staged</span>
                            <span class="changes-section-count">{sortedStagedEntries.length}</span>
                        </button>
                        {#if !collapsedSections.has('staged-files')}
                        <div class="changes-section-body">
                            {#each sortedStagedEntries as entry}
                                {@const { folder, name } = splitPath(entry.path)}
                                <button
                                    class="changes-file-item"
                                    class:selected={selectedPath === entry.path}
                                    onclick={() => selectedPath = entry.path}
                                    title={entry.path}
                                >
                                    <span class="changes-file-basename">{name}</span>
                                    {#if folder}<span class="changes-file-folder">{folder}</span>{/if}
                                    <span class="changes-file-badge" style="color: {getStatusBadge(entry).color}" title={getStatusLabel(entry.status)}>
                                        {entry.status}
                                    </span>
                                </button>
                            {/each}
                        </div>
                        {/if}
                    </div>
                    {/if}

                    <!-- Unstaged entries -->
                    {#if sortedUnstagedEntries.length > 0}
                    <div class="changes-section">
                        <button class="changes-section-header" type="button" onclick={() => toggleSection('unstaged-files')} aria-expanded={!collapsedSections.has('unstaged-files')}>
                            <svg class="changes-chevron" class:expanded={!collapsedSections.has('unstaged-files')} width="10" height="10" viewBox="0 0 16 16" fill="none">
                                <path d="M6 4l4 4-4 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                            </svg>
                            <span>Unstaged</span>
                            <span class="changes-section-count">{sortedUnstagedEntries.length}</span>
                        </button>
                        {#if !collapsedSections.has('unstaged-files')}
                        <div class="changes-section-body">
                            {#each sortedUnstagedEntries as entry}
                                {@const { folder, name } = splitPath(entry.path)}
                                <button
                                    class="changes-file-item"
                                    class:selected={selectedPath === entry.path}
                                    onclick={() => selectedPath = entry.path}
                                    title={entry.path}
                                >
                                    <span class="changes-file-basename">{name}</span>
                                    {#if folder}<span class="changes-file-folder">{folder}</span>{/if}
                                    <span class="changes-file-badge" style="color: {getStatusBadge(entry).color}" title={getStatusLabel(entry.status)}>
                                        {entry.status === '?' ? '??' : entry.status}
                                    </span>
                                </button>
                            {/each}
                        </div>
                        {/if}
                    </div>
                    {/if}
                </div>
            </div>

            <!-- Resize handle: sidebar ↔ diff -->
            {#if !isMobile}
            <ResizeHandle
                ariaLabel="Resize changes sidebar"
                onDragStart={handleDragStart}
                onDragMove={handleDragMove}
                onDragEnd={handleDragEnd}
            />
            {/if}

            <!-- Right: Diff viewer -->
            <div class="changes-diff">
                <DiffViewer
                    stagedFiles={sortedStagedFiles}
                    unstagedFiles={sortedUnstagedFiles}
                    selectedPath={selectedPath}
                />
            </div>
        </div>
    {/if}
</div>

<style>
    .git-status-panel {
        display: flex;
        flex-direction: column;
        flex: 1;
        min-height: 0;
        overflow: hidden;
        background: var(--color-surface-1);
    }

    /* Header */
    .panel-header {
        display: flex;
        align-items: center;
        gap: 6px;
        padding: 6px 10px;
        border-bottom: 1px solid var(--color-border);
        flex-shrink: 0;
        background: var(--color-surface-2);
    }
    .panel-title {
        font-size: 0.75rem;
        font-weight: 600;
        color: var(--color-text-secondary);
        text-transform: uppercase;
        letter-spacing: 0.05em;
        flex: 1;
    }
    .panel-icon-btn {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 22px;
        height: 22px;
        padding: 0;
        border: none;
        background: transparent;
        color: var(--color-text-secondary);
        cursor: pointer;
        border-radius: 4px;
    }
    .panel-icon-btn:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }

    /* Loading / Empty */
    .panel-loading,
    .panel-empty {
        display: flex;
        flex-direction: column;
        align-items: center;
        justify-content: center;
        gap: 12px;
        padding: 48px 24px;
        color: var(--color-text-secondary);
        font-size: 0.875rem;
        flex: 1;
        min-height: 0;
    }
    .panel-loading .spinner {
        width: 20px;
        height: 20px;
        border: 2px solid var(--color-text-secondary);
        border-top-color: transparent;
        border-radius: 50%;
        animation: spin 0.6s linear infinite;
    }
    @keyframes spin { to { transform: rotate(360deg); } }

    /* Body: two-column layout */
    .panel-body {
        display: flex;
        flex: 1;
        overflow: hidden;
        min-height: 0;
    }

    /* Changes sidebar */
    .changes-sidebar {
        flex-shrink: 0;
        border-right: 1px solid var(--color-border);
        background: var(--color-surface-1);
        display: flex;
        flex-direction: column;
        overflow: hidden;
    }
    .changes-scroll {
        flex: 1;
        overflow-y: auto;
        overflow-x: hidden;
        padding: 4px 0;
    }

    /* Sections */
    .changes-section {
        margin-bottom: 2px;
    }
    .changes-section-header {
        display: flex;
        align-items: center;
        gap: 4px;
        width: 100%;
        padding: 4px 8px;
        border: none;
        background: transparent;
        color: var(--color-text-secondary);
        font-size: 0.6875rem;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.04em;
        cursor: pointer;
        text-align: left;
    }
    .changes-section-header:hover {
        color: var(--color-text-primary);
    }
    .changes-chevron {
        flex-shrink: 0;
        color: var(--color-text-tertiary);
        transition: transform 0.15s ease;
    }
    .changes-chevron.expanded {
        transform: rotate(90deg);
    }
    .changes-section-count {
        margin-left: auto;
        font-size: 0.6875rem;
        font-weight: 500;
        color: var(--color-text-tertiary);
    }
    .changes-section-body {
        padding: 0 4px;
    }

    /* File items */
    .changes-file-item {
        display: flex;
        align-items: center;
        gap: 6px;
        width: 100%;
        padding: 3px 6px;
        border: none;
        background: transparent;
        color: var(--color-text-primary);
        font-size: 0.75rem;
        cursor: pointer;
        text-align: left;
        border-radius: 4px;
    }
    .changes-file-item:hover {
        background: var(--color-surface-3);
    }
    .changes-file-item.selected {
        background: var(--color-surface-4);
    }
    .changes-file-basename {
        color: var(--color-text-primary);
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
        font-family: var(--font-mono);
        flex-shrink: 0;
    }
    .changes-file-folder {
        color: var(--color-text-tertiary);
        opacity: 0.6;
        font-family: var(--font-mono);
        flex: 1;
        min-width: 0;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
        text-align: right;
        padding: 0 6px;
    }
    .changes-file-badge {
        font-size: 0.6875rem;
        font-weight: 700;
        font-family: var(--font-mono);
        flex-shrink: 0;
        width: 1.5rem;
        text-align: center;
        user-select: none;
    }

    /* Diff viewer area */
    .changes-diff {
        flex: 1;
        min-width: 0;
        display: flex;
        flex-direction: column;
        overflow: hidden;
    }

    /* Mobile: hide sidebar, show only diff */
    .changes-sidebar.hidden {
        display: none;
    }

    @media (max-width: 768px) {
        .changes-sidebar {
            display: none;
        }
        .changes-sidebar:not(.hidden) {
            display: flex;
        }
        .panel-body {
            flex-direction: column;
        }
    }
</style>
