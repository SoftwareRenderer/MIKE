<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import type { DiffFile } from '$lib/types/diff';
    import { gitStatusLabel, gitStatusAbsentFromHead } from '$lib/utils/git-status';
    import { confirmList } from '$lib/utils/file-selection';
    import { t } from '$lib/i18n';
    import { getGitStatus, getProjectDiffFiles, getCommitDiffFiles, getProjectDiffFile, stageProjectFile, commitProject, discardProjectFile, pullProject, pushProject, getGitLogFull } from '$lib/api';
    import type { GitCommit } from '$lib/types';
    import { dialogStore } from '$lib/stores';
    import { loadAppSetting, saveAppSetting } from '$lib/storage';
    import { useFileSelection } from '$lib/composables/use-file-selection.svelte';
    import { useLazyComponent } from '$lib/composables/use-lazy-component.svelte';
    import FileContextMenu, { type FileMenuItem } from './FileContextMenu.svelte';
    import SelectionActionBar from './SelectionActionBar.svelte';
    import Icon from './Icon.svelte';
    import stageIcon from '$lib/assets/icons/stage.svg?raw';
    import unstageIcon from '$lib/assets/icons/unstage.svg?raw';
    import discardIcon from '$lib/assets/icons/discard.svg?raw';
    import ResizeHandle from './ResizeHandle.svelte';

    const diffViewer = useLazyComponent(() => import('./DiffViewer.svelte'));

    let { projectId, projectPath, onClose, isMobile, onCommitted, onUpdated, onEditFile }: {
        projectId: string;
        projectPath: string;
        onClose: () => void;
        isMobile: boolean;
        onCommitted?: () => void;
        onUpdated?: () => void;
        onEditFile?: (path: string, line?: number) => void;
    } = $props();

    // Parsed status entries for the sidebar file list
    interface StatusEntry {
        path: string;
        status: string; // M, A, D, ?, R, U, C
    }

    // Untracked file entry for the DiffViewer (content loaded on-demand)
    interface UntrackedEntry {
        path: string;
        fileSize?: number;
        isBinary?: boolean;
        truncated?: boolean;
        origAdded?: number;
        loading?: boolean;
        file: DiffFile;
    }

    let stagedEntries = $state<StatusEntry[]>([]);
    let unstagedEntries = $state<StatusEntry[]>([]);

    // Diff data for DiffViewer
    let stagedDiffFiles = $state<DiffFile[]>([]);
    let unstagedDiffFiles = $state<DiffFile[]>([]);
    let unstagedUntracked = $state<UntrackedEntry[]>([]);

    let loading = $state(true);
    let selectedPath = $state('');
    let headHash = $state('');
    // Git author identity for commit dialog
    let authorName = $state('');
    let authorEmail = $state('');

    // Commit comparison state
    const EMPTY_TREE = '4b825dc642cb6eb9a060e54bf899d69f826635b6';
    // Sentinel value for "Current" (working tree) in the From dropdown
    const CURRENT_TREE = '__current__';
    let commits = $state<GitCommit[]>([]);
    let fromCommit = $state('');
    let toCommit = $state('');
    let compareDiff = $state<DiffFile[]>([]);
    let compareLoading = $state(false);
    let compareError = $state('');
    let showFromDropdown = $state(false);
    let showToDropdown = $state(false);
    // svelte-ignore non_reactive_update — DOM refs via bind:this
    let fromDropdownEl: HTMLDivElement | null = null;
    // svelte-ignore non_reactive_update — DOM refs via bind:this
    let toDropdownEl: HTMLDivElement | null = null;
    // Combobox input state for "From" field (allows typing a hash)
    let fromInputValue = $state('');
    let fromInputFocused = $state(false);
    // svelte-ignore non_reactive_update — DOM ref via bind:this
    let fromInputEl: HTMLInputElement | null = null;
    // Filtered commits list for the "From" combobox
    let filteredFromCommits = $derived.by(() => {
        const query = fromInputValue.trim().toLowerCase();
        if (!query) return commits;
        // If query matches the current selection's short hash, show all commits
        const currentFrom = commits.find(c => c.hash === fromCommit);
        if (currentFrom && query === currentFrom.short.toLowerCase()) return commits;
        return commits.filter(c =>
            c.hash.toLowerCase().includes(query) ||
            c.short.toLowerCase().includes(query) ||
            c.subject.toLowerCase().includes(query)
        );
    });
    // Combobox input state for "To" field (allows typing a hash)
    let toInputValue = $state('');
    // svelte-ignore non_reactive_update — DOM ref via bind:this
    let toInputEl: HTMLInputElement | null = null;
    let toInputFocused = $state(false);
    // Filtered commits list for the "To" combobox
    let filteredToCommits = $derived.by(() => {
        const query = toInputValue.trim().toLowerCase();
        if (!query) return commits;
        // If query matches the current selection's short hash, show all commits
        const currentTo = commits.find(c => c.hash === toCommit);
        if (currentTo && query === currentTo.short.toLowerCase()) return commits;
        return commits.filter(c =>
            c.hash.toLowerCase().includes(query) ||
            c.short.toLowerCase().includes(query) ||
            c.subject.toLowerCase().includes(query)
        );
    });

    function toggleFromDropdown() {
        showFromDropdown = !showFromDropdown;
        showToDropdown = false;
        if (showFromDropdown) fromInputEl?.focus();
    }
    function toggleToDropdown() {
        showToDropdown = !showToDropdown;
        showFromDropdown = false;
        if (showToDropdown) toInputEl?.focus();
    }
    function closeDropdowns() {
        showFromDropdown = false;
        showToDropdown = false;
    }

    // Keyboard navigation for dropdown listboxes (Arrow Up/Down, Enter, Escape)
    let activeFromIndex = $state(-1);
    let activeToIndex = $state(-1);

    function handleFromKeydown(e: KeyboardEvent) {
        const items = fromDropdownEl?.querySelectorAll('.commit-dropdown-item') as NodeListOf<HTMLButtonElement> | undefined;
        if (e.key === 'Enter') {
            e.preventDefault();
            // 1. If an item is highlighted, select it
            if (items && activeFromIndex >= 0 && activeFromIndex < items.length) {
                items[activeFromIndex]?.click();
            } else {
                // 2. Otherwise, use the raw input value directly
                const trimmed = fromInputValue.trim();
                if (trimmed) {
                    if (trimmed.toLowerCase() === 'current') {
                        fromCommit = CURRENT_TREE;
                    } else {
                        const matched = commits.find(c => c.hash === trimmed || c.short === trimmed);
                        fromCommit = matched ? matched.hash : trimmed;
                    }
                }
                closeDropdowns();
                fromInputEl?.blur();
            }
            return;
        }
        if (!items || items.length === 0) {
            if (e.key === 'ArrowDown') {
                e.preventDefault();
                showFromDropdown = true;
                showToDropdown = false;
            }
            return;
        }
        if (e.key === 'ArrowDown') {
            e.preventDefault();
            showFromDropdown = true;
            showToDropdown = false;
            activeFromIndex = (activeFromIndex + 1) % items.length;
            items[activeFromIndex]?.focus();
        } else if (e.key === 'ArrowUp') {
            e.preventDefault();
            showFromDropdown = true;
            showToDropdown = false;
            activeFromIndex = (activeFromIndex - 1 + items.length) % items.length;
            items[activeFromIndex]?.focus();
        } else if (e.key === 'Escape') {
            e.preventDefault();
            closeDropdowns();
            fromInputEl?.focus();
        }
    }

    function handleToKeydown(e: KeyboardEvent) {
        const items = toDropdownEl?.querySelectorAll('.commit-dropdown-item') as NodeListOf<HTMLButtonElement> | undefined;
        if (e.key === 'Enter') {
            e.preventDefault();
            // 1. If an item is highlighted, select it
            if (items && activeToIndex >= 0 && activeToIndex < items.length) {
                items[activeToIndex]?.click();
            } else {
                // 2. Otherwise, use the raw input value directly
                const trimmed = toInputValue.trim();
                if (trimmed) {
                    const matched = commits.find(c => c.hash === trimmed || c.short === trimmed);
                    toCommit = matched ? matched.hash : trimmed;
                }
                closeDropdowns();
                toInputEl?.blur();
            }
            return;
        }
        if (!items || items.length === 0) {
            if (e.key === 'ArrowDown') {
                e.preventDefault();
                showToDropdown = true;
                showFromDropdown = false;
            }
            return;
        }
        if (e.key === 'ArrowDown') {
            e.preventDefault();
            showToDropdown = true;
            showFromDropdown = false;
            activeToIndex = (activeToIndex + 1) % items.length;
            items[activeToIndex]?.focus();
        } else if (e.key === 'ArrowUp') {
            e.preventDefault();
            showToDropdown = true;
            showFromDropdown = false;
            activeToIndex = (activeToIndex - 1 + items.length) % items.length;
            items[activeToIndex]?.focus();
        } else if (e.key === 'Escape') {
            e.preventDefault();
            closeDropdowns();
            toInputEl?.focus();
        }
    }

    function handleListItemFocus(index: number, dropdown: 'from' | 'to') {
        if (dropdown === 'from') {
            activeFromIndex = index >= 0 ? index + 1 : 0; // +1 for "Current" button offset
        } else {
            activeToIndex = index;
        }
    }

    // Handle typed input in the "From" combobox
    function handleFromInput(e: Event) {
        const value = (e.target as HTMLInputElement).value;
        fromInputValue = value;
        showFromDropdown = true;
        showToDropdown = false;
        activeFromIndex = -1;
    }

    function handleFromInputBlur() {
        fromInputFocused = false;
        // Delay to allow click on dropdown item to fire first
        setTimeout(() => {
            // Keep dropdown open if focus moved to a dropdown item (arrow key navigation)
            if (fromDropdownEl && fromDropdownEl.contains(document.activeElement)) return;
            showFromDropdown = false;
            const trimmed = fromInputValue.trim();
            if (!trimmed) return;
            // Handle "Current" sentinel
            if (trimmed.toLowerCase() === 'current') {
                fromCommit = CURRENT_TREE;
                return;
            }
            // If input value doesn't match any commit, keep it as a raw hash
            const matched = commits.find(c => c.hash === trimmed || c.short === trimmed);
            fromCommit = matched ? matched.hash : trimmed;
        }, 150);
    }

    function handleFromInputFocus() {
        fromInputFocused = true;
        showFromDropdown = true;
        showToDropdown = false;
    }

    // Handle typed input in the "To" combobox
    function handleToInput(e: Event) {
        const value = (e.target as HTMLInputElement).value;
        toInputValue = value;
        showToDropdown = true;
        showFromDropdown = false;
        activeToIndex = -1;
    }

    function handleToInputBlur() {
        toInputFocused = false;
        // Delay to allow click on dropdown item to fire first
        setTimeout(() => {
            // Keep dropdown open if focus moved to a dropdown item (arrow key navigation)
            if (toDropdownEl && toDropdownEl.contains(document.activeElement)) return;
            showToDropdown = false;
            // If input value doesn't match any commit, keep it as a raw hash
            const trimmed = toInputValue.trim();
            if (trimmed) {
                const matched = commits.find(c => c.hash === trimmed || c.short === trimmed);
                toCommit = matched ? matched.hash : trimmed;
            }
        }, 150);
    }

    function handleToInputFocus() {
        toInputFocused = true;
        showToDropdown = true;
        showFromDropdown = false;
    }
    // Close dropdowns on outside click
    $effect(() => {
        function handleClick(e: MouseEvent) {
            if (fromDropdownEl && !fromDropdownEl.contains(e.target as Node) &&
                toDropdownEl && !toDropdownEl.contains(e.target as Node)) {
                closeDropdowns();
            }
        }
        if (showFromDropdown || showToDropdown) {
            document.addEventListener('click', handleClick);
            return () => document.removeEventListener('click', handleClick);
        }
    });

    async function loadCommits() {
        try {
            const resp = await getGitLogFull(projectId, { limit: 50 });
            commits = resp.commits;
            // Set To = HEAD; From is set by updateCompareMode based on context
            if (commits.length > 0) {
                toCommit = commits[0].hash;
            }
        } catch (e) {
            console.error('Failed to load commits:', e);
        }
    }

    async function fetchCompareDiff() {
        if (!fromCommit || !toCommit) return;
        // Skip fetch when "Current" (working tree) is selected — working tree diff is shown directly
        if (fromCommit === CURRENT_TREE) return;
        compareLoading = true;
        compareError = '';
        compareDiff = [];
        // Preload the diff viewer chunk
        diffViewer.load();
        try {
            compareDiff = await getCommitDiffFiles(projectId, fromCommit, toCommit);
        } catch (e: any) {
            compareError = e.message || t('git.fetchDiffFailed');
            compareDiff = [];
        } finally {
            compareLoading = false;
        }
    }

    // Fetch diff when from/to change
    $effect(() => {
        fromCommit;
        toCommit;
        if (fromCommit && toCommit) {
            fetchCompareDiff();
        }
    });

    // Sync input values when commit hashes change (e.g. initial load)
    // Only sync when the input is NOT focused so we don't fight user typing
    $effect(() => {
        fromCommit;
        if (!fromInputFocused) {
            if (fromCommit === CURRENT_TREE) {
                fromInputValue = t('git.current');
            } else {
                const commit = commits.find(c => c.hash === fromCommit);
                fromInputValue = commit ? commit.short : fromCommit.substring(0, 7);
            }
        }
    });
    $effect(() => {
        toCommit;
        if (!toInputFocused) {
            const commit = commits.find(c => c.hash === toCommit);
            toInputValue = commit ? commit.short : toCommit.substring(0, 7);
        }
    });

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
        unstagedEntries.length > 0 ||
        unstagedUntracked.length > 0 ||
        compareDiff.length > 0 ||
        compareLoading
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

    // ── The sidebar's rows, their selection, and the menu that acts on them ──

    // loadData lists a path in one section only, so a path names one row and can key the selection.

    type ChangesSection = 'staged' | 'unstaged';

    interface ChangesRow {
        path: string;
        status: string;
        /** Also the boundary a shift range stops at. */
        group: ChangesSection;
        /** Position in the flat row order; the selection addresses rows by index. */
        index: number;
        /** Always true: the row type is shared with the Explorer, whose listing has a `..` row. */
        selectable: true;
    }

    interface ChangesGroup {
        id: ChangesSection;
        label: string;
        allLabel: string;
        run: () => void;
        rows: ChangesRow[];
    }

    let groups = $derived.by((): ChangesGroup[] => {
        const flat: ChangesRow[] = [];
        const sections: [ChangesSection, StatusEntry[]][] = [
            ['staged', sortedStagedEntries],
            ['unstaged', sortedUnstagedEntries],
        ];
        return sections.map(([id, entries]) => {
            const list = entries.map(entry => {
                const row: ChangesRow = { path: entry.path, status: entry.status, group: id, index: flat.length, selectable: true };
                flat.push(row);
                return row;
            });
            return {
                id,
                label: t(id === 'staged' ? 'diff.staged' : 'diff.unstaged'),
                allLabel: t(id === 'staged' ? 'git.unstageAll' : 'git.stageAll'),
                run: () => handleStagePaths(list.map(row => row.path), id !== 'staged'),
                rows: list,
            };
        });
    });

    /** In render order, which is the order a shift range runs across. */
    let rows = $derived(groups.flatMap(group => group.rows));

    const selection = useFileSelection({ rows: () => rows });

    let selectedRows = $derived(rows.filter(row => selection.isSelected(row.path)));
    /** Stage takes the working-tree rows, Unstage the indexed ones; a mixed selection has both. */
    let selectedBySection = $derived.by(() => {
        const of = (group: ChangesSection) => selectedRows.filter(row => row.group === group).map(row => row.path);
        return { staged: of('staged'), unstaged: of('unstaged') };
    });

    let menu = $state<{ x: number; y: number; onRow: boolean } | null>(null);

    function handleRowClick(e: MouseEvent, row: ChangesRow): void {
        // A plain click moves the diff; ctrl and shift only change the selection.
        if (selection.handleClick(e, row.index)) selectedPath = row.path;
    }

    function openMenuAt(e: MouseEvent | KeyboardEvent, x: number, y: number, row: ChangesRow): void {
        e.preventDefault();
        selection.selectRow(row.index);
        menu = { x, y, onRow: true };
    }

    // The space below the rows is where the section-wide actions live.
    function handleListContextmenu(e: MouseEvent): void {
        if ((e.target as HTMLElement).closest('.changes-file-row')) return;
        e.preventDefault();
        menu = { x: e.clientX, y: e.clientY, onRow: false };
    }

    const menuItems = $derived.by((): FileMenuItem[] => {
        const asked = menu;
        if (!asked) return [];
        if (!asked.onRow) {
            return groups
                .filter(group => group.rows.length > 0)
                .map((group): FileMenuItem => ({
                    label: group.allLabel,
                    icon: group.id === 'staged' ? 'unstage' : 'stage',
                    run: group.run,
                }));
        }
        return [
            {
                label: t('git.stage'),
                icon: 'stage',
                disabled: selectedBySection.unstaged.length === 0,
                run: () => handleStagePaths(selectedBySection.unstaged, true),
            },
            {
                label: t('git.unstage'),
                icon: 'unstage',
                disabled: selectedBySection.staged.length === 0,
                run: () => handleStagePaths(selectedBySection.staged, false),
            },
            {
                label: t('explorer.shared.discard'),
                icon: 'discard',
                destructive: true,
                disabled: selectedRows.length === 0,
                run: () => handleDiscardRows(selectedRows),
            },
        ];
    });

    function handleListKeydown(e: KeyboardEvent): void {
        const key = selection.handleKeydown(e);
        if (key?.kind === 'handled') return;
        if (key?.kind === 'menu') {
            const row = rows[key.index];
            if (row) openMenuAt(e, key.at.x, key.at.y, row);
        }
    }

    async function loadData(silent = false): Promise<boolean> {
        // Silent reloads (triggered by stage/unstage) update the panel in place
        if (!silent) loading = true;
        // Preload the diff viewer chunk while fetching data
        diffViewer.load();
        try {
            // Fetch git status for sidebar
            const { status, head, authorName: author, authorEmail: email, ahead, behind } = await getGitStatus(projectId);
            headHash = head;
            authorName = author;
            authorEmail = email;
            aheadCommits = ahead;
            behindCommits = behind;
            const staged: StatusEntry[] = [];
            const unstaged: StatusEntry[] = [];
            const untrackedPaths: string[] = [];

            for (const line of status.split('\n').filter(l => l.trim())) {
                const indexChar = line[0];
                const workChar = line[1];
                const pathPart = line.substring(3).trim();

                const arrowIdx = pathPart.indexOf(' -> ');
                const filePath = arrowIdx >= 0 ? pathPart.substring(arrowIdx + 4) : pathPart;

                // Index changes (staged)
                if (indexChar !== ' ' && indexChar !== '?') {
                    staged.push({ path: filePath, status: indexChar });
                }
                // Working tree changes (unstaged)
                if (workChar === '?') {
                    unstaged.push({ path: filePath, status: '?' });
                    untrackedPaths.push(filePath);
                } else if (workChar !== ' ' && workChar !== indexChar) {
                    const existing = staged.find(s => s.path === filePath);
                    if (!existing) {
                        unstaged.push({ path: filePath, status: workChar });
                    }
                }
            }

            stagedEntries = staged;
            unstagedEntries = unstaged;

            // Fetch diffs for DiffViewer and metadata for untracked files
            const [stagedFiles, unstagedFiles, untrackedMeta] = await Promise.all([
                getProjectDiffFiles(projectId, { cached: true }).catch(() => []),
                getProjectDiffFiles(projectId).catch(() => []),
                Promise.all(untrackedPaths.map(async (path) => {
                    try {
                        return { path, meta: await getProjectDiffFile(projectId, path, { meta: true }) };
                    } catch {
                        return { path, meta: undefined };
                    }
                })),
            ]);

            stagedDiffFiles = stagedFiles;
            unstagedDiffFiles = unstagedFiles;
            unstagedUntracked = untrackedMeta.flatMap(({ path, meta }) => {
                if (!meta) return [];
                const added = meta.line_count ?? 0;
                return [{
                    path,
                    fileSize: meta.file_size,
                    isBinary: meta.isBinary,
                    origAdded: added,
                    file: { path, rows: [], added, removed: 0 },
                }];
            });

            return stagedEntries.length > 0 || unstagedEntries.length > 0;
        } catch (e) {
            console.error('Failed to load git status:', e);
            return false;
        } finally {
            if (!silent) loading = false;
        }
    }

    /** Set default From/To based on whether there are working tree changes.
     * When there are changes, default to "Current" (working tree) vs latest commit.
     * When clean, default to previous commit vs latest commit. */
    async function updateCompareMode(hasChanges: boolean) {
        // Always ensure commits are loaded (for dropdown population)
        if (commits.length === 0) {
            await loadCommits();
        }
        if (hasChanges) {
            // Default to "Current" (working tree) when there are changes
            fromCommit = CURRENT_TREE;
            fromInputValue = t('git.current');
        } else {
            // Default to HEAD~1 → HEAD when clean
            if (fromCommit === CURRENT_TREE || !fromCommit) {
                fromCommit = commits[0]?.parents?.[0] || (commits.length > 1 ? commits[1].hash : EMPTY_TREE);
                fromInputValue = commits.find(c => c.hash === fromCommit)?.short || fromCommit.substring(0, 7);
            }
        }
        // Sync To input
        toInputValue = commits.find(c => c.hash === toCommit)?.short || toCommit.substring(0, 7);
    }

    /** Load the full content of an untracked file on-demand for the DiffViewer. */
    async function loadUntrackedFile(path: string) {
        const entry = unstagedUntracked.find(e => e.path === path);
        if (!entry || entry.loading) return;
        entry.loading = true;
        try {
            const data = await getProjectDiffFile(projectId, path);
            if (data.lines) {
                entry.file = {
                    path,
                    rows: data.lines.map((line, i) => ({ lineNumber: i + 1, variant: 'added' as const, text: line })),
                    added: data.line_count ?? data.lines.length,
                    removed: 0,
                    isBinary: false,
                };
            } else if (data.isBinary) {
                entry.file = { path, rows: [], added: data.line_count ?? 0, removed: 0, isBinary: true, fileSize: data.file_size };
            }
        } catch (e) {
            console.error('Failed to load untracked file:', e);
        } finally {
            entry.loading = false;
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

    // Serialize the panel's git writes: each one reloads the panel, so two in flight would read each
    // other's half-applied state.
    let gitWriteQueue: Promise<void> = Promise.resolve();
    function enqueueGitWrite(task: () => Promise<void>): void {
        gitWriteQueue = gitWriteQueue.then(task).catch(() => {});
    }

    /** Reread the panel after a git write, and move the compare selectors to what git now reports. */
    async function refreshAfterWrite(): Promise<void> {
        const hasChanges = await loadData(true);
        await updateCompareMode(hasChanges);
    }

    /** One request per path; the git endpoints take a single file. A refusal stops nothing, since the
     *  paths are independent and the reload reads git's own state. */
    async function writeEach(
        paths: string[],
        write: (path: string) => Promise<unknown>,
    ): Promise<{ landed: number; failed: number; reason: string }> {
        let landed = 0;
        let failed = 0;
        let reason = '';
        for (const path of paths) {
            try {
                await write(path);
                landed++;
            } catch (e) {
                failed++;
                if (e instanceof Error) reason = e.message;
            }
        }
        return { landed, failed, reason };
    }

    /** Stage or unstage every path given, then reload once. Refusals are reported by count and git's
     *  first reason. */
    function handleStagePaths(paths: string[], stage: boolean): void {
        if (paths.length === 0) return;
        enqueueGitWrite(async () => {
            const { landed, failed, reason } = await writeEach(paths, path => stageProjectFile(projectId, path, stage));
            if (landed > 0) await refreshAfterWrite();
            if (failed > 0) {
                dialogStore.showToast(t(stage ? 'git.stageFailed' : 'git.unstageFailed', { count: failed, error: reason }), 'error');
            }
        });
    }

    function pathsOf(section: ChangesSection): string[] {
        return groups.find(group => group.id === section)?.rows.map(row => row.path) ?? [];
    }

    /** Revert every row to HEAD, or delete the ones HEAD never held. A discard cannot be undone, so the
     * confirm dialog is the only guard. */
    async function handleDiscardRows(targets: ChangesRow[]): Promise<void> {
        if (targets.length === 0) return;
        const paths = targets.map(row => row.path);
        const message = targets.length > 1
            ? t('git.discardBatchMessage')
            : gitStatusAbsentFromHead(targets[0].status)
                ? t('git.discardUntrackedMessage')
                : t('git.discardTrackedMessage');
        const proceed = await dialogStore.showConfirm({
            title: t('git.discardTitle'),
            message,
            items: confirmList(paths, count => t('explorer.shared.deleteMore', { count })),
            confirmText: t('explorer.shared.discard'),
            cancelText: t('git.cancel'),
            variant: 'destructive',
        });
        if (!proceed) return;
        enqueueGitWrite(async () => {
            const { landed, failed, reason } = await writeEach(paths, path => discardProjectFile(projectId, path));
            if (landed === 0) {
                // Nothing moved, so there is nothing to reread.
                dialogStore.showToast(paths.length === 1
                    // One refusal: git's own words are the whole report.
                    ? reason || t('git.discardFailed')
                    : t('git.discardFailedCount', { count: failed, error: reason }), 'error');
                return;
            }
            // The diff being shown may be one of these.
            if (paths.includes(selectedPath)) selectedPath = '';
            await refreshAfterWrite();
            dialogStore.showToast(
                failed === 0
                    ? t(paths.length === 1 ? 'git.discarded' : 'git.discardedCount', { count: paths.length })
                    : t('git.discardFailedCount', { count: failed, error: reason }),
                failed === 0 ? 'success' : 'error',
            );
        });
    }

    // Pull/Push in-flight state (guards against re-entry and shows spinners)
    let pulling = $state(false);
    let pushing = $state(false);

    // The counts come from the upstream's remote-tracking ref, so they move when something fetches
    // rather than when the remote moves. `remoteFresh` is set by a pull or a push, and the `~`
    // prefix marks everything counted before that.
    let aheadCommits = $state(0);
    let behindCommits = $state(0);
    let remoteFresh = $state(false);

    const countMarker = $derived(remoteFresh ? '' : '~');
    const syncNote = $derived(remoteFresh ? '' : ` — ${t('git.countsAsOfLastSync')}`);
    const pullLabel = $derived(`${t('git.pullFromRemote')} (${t('git.commitsBehind', { count: behindCommits, marker: countMarker })})${syncNote}`);
    const pushLabel = $derived(`${t('git.pushToRemote')} (${t('git.commitsAhead', { count: aheadCommits, marker: countMarker })})${syncNote}`);

    /** Pull from the remote, then refresh the panel in place. */
    async function handlePull() {
        if (pulling || pushing) return;
        pulling = true;
        try {
            await pullProject(projectId);
            remoteFresh = true;
            dialogStore.showToast(t('git.pulledFromRemote'), 'success');
            const hasChanges = await loadData(true);
            await updateCompareMode(hasChanges);
            onUpdated?.();
        } catch (e: any) {
            dialogStore.showToast(e.message || t('git.pullFailed'), 'error');
        } finally {
            pulling = false;
        }
    }

    /** Push to the remote, then refresh the panel in place. */
    async function handlePush() {
        if (pulling || pushing) return;
        pushing = true;
        try {
            await pushProject(projectId);
            // A push that was not rejected proves the branch is not behind and leaves nothing ahead.
            remoteFresh = true;
            dialogStore.showToast(t('git.pushedToRemote'), 'success');
            const hasChanges = await loadData(true);
            await updateCompareMode(hasChanges);
            onUpdated?.();
        } catch (e: any) {
            dialogStore.showToast(e.message || t('git.pushFailed'), 'error');
        } finally {
            pushing = false;
        }
    }

    /** Open the commit dialog, with HEAD staleness check first. */
    async function openCommitDialog() {
        // Check if HEAD has changed since we last loaded data
        if (headHash) {
            try {
                const { head: currentHead } = await getGitStatus(projectId);
                if (currentHead !== headHash) {
                    const proceed = await dialogStore.showConfirm({
                        title: t('git.repoChangedTitle'),
                        message: t('git.repoChangedMessage'),
                        confirmText: t('git.refreshAndCommit'),
                        cancelText: t('git.commitAnyway'),
                    });
                    if (proceed) {
                        const hasChanges = await loadData();
                        await updateCompareMode(hasChanges);
                    }
                }
            } catch {
                // If status check fails, proceed with commit anyway
            }
        }
        const message = await dialogStore.showCommitDialog({
            message: '',
            confirmText: t('git.commit'),
            cancelText: t('git.cancel'),
            authorName: authorName,
            authorEmail: authorEmail,
        });
        if (!message) return;
        try {
            await commitProject(projectId, message.trim());
            onCommitted?.();
            onClose();
        } catch (e: any) {
            dialogStore.showToast(e.message || t('git.commitFailed'), 'error');
        }
    }

    /** Handle line click from DiffViewer — open file in the shared file editor via the explorer. */
    function handleLineClick(filePath: string, lineNumber: number) {
        // Build absolute path from project root + relative diff path
        const absPath = projectPath.endsWith('/') ? projectPath + filePath : projectPath + '/' + filePath;
        if (onEditFile) onEditFile(absPath, lineNumber);
    }

    function handleKeydown(e: KeyboardEvent) {
        if (e.key !== 'Escape') return;
        // Rows a person is holding get the key first: giving them back is not "close the panel".
        if (selection.releaseSelection()) return;
        onClose();
    }

    onMount(() => {
        loadData().then((hasChanges) => updateCompareMode(hasChanges));
        window.addEventListener('keydown', handleKeydown);
    });

    onDestroy(() => {
        window.removeEventListener('keydown', handleKeydown);
    });
</script>

<div class="git-status-panel">
    <!-- Header -->
    <div class="panel-header">
        <button class="close-btn" onclick={onClose} aria-label="Close changes panel" title="Close (Escape)">
            <svg width="12" height="12" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                <path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
            </svg>
        </button>
        <span class="panel-title">{t('git.changes')}</span>
        <!-- Commit comparison dropdowns -->
        <div class="commit-selectors" role="group" aria-label="Compare commits">
            {#snippet commitList(commits: GitCommit[], selected: string, idBase: string, query: string, onPick: (commit: GitCommit) => void, onKeydown: (e: KeyboardEvent) => void, onFocusItem: (index: number) => void)}
                {#each commits as commit, i}
                    <button
                        id={`${idBase}-item-${i}`}
                        class="commit-dropdown-item"
                        class:active={commit.hash === selected}
                        onclick={() => onPick(commit)}
                        onkeydown={onKeydown}
                        onfocus={() => onFocusItem(i)}
                        role="option"
                        aria-selected={commit.hash === selected}
                    >
                        {#if commit.hash === selected}
                            <svg width="8" height="8" viewBox="0 0 8 8" fill="none" aria-hidden="true">
                                <polyline points="1.5,4 3,5.5 6.5,2" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                            </svg>
                        {:else}
                            <span class="commit-check-empty"></span>
                        {/if}
                        <span class="commit-dropdown-hash">{commit.short}</span>
                        <span class="commit-dropdown-subject" title={commit.subject}>{commit.subject}</span>
                    </button>
                {/each}
                {#if commits.length === 0 && query.trim()}
                    <div class="commit-dropdown-empty">{t('git.noMatchingCommits')}</div>
                {/if}
            {/snippet}
            <div class="commit-selector" class:open={showFromDropdown} bind:this={fromDropdownEl}>
                <div class="commit-combobox">
                    <svg class="commit-combobox-icon" width="10" height="10" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                        <circle cx="8" cy="8" r="5" stroke="currentColor" stroke-width="1.5"/>
                        <path d="M8 5v6M5 8h6" stroke="currentColor" stroke-width="1.2" stroke-linecap="round"/>
                    </svg>
                    <input
                        id="compare-from-input"
                        bind:this={fromInputEl}
                        type="text"
                        class="commit-combobox-input"
                        placeholder="commit hash or subject"
                        value={fromInputValue}
                        oninput={handleFromInput}
                        onkeydown={handleFromKeydown}
                        onblur={handleFromInputBlur}
                        onfocus={handleFromInputFocus}
                        aria-label="From commit"
                        aria-expanded={showFromDropdown}
                        aria-controls="compare-from-listbox"
                        aria-autocomplete="list"
                        role="combobox"
                        autocomplete="off"
                    />
                    <button
                        type="button"
                        class="commit-combobox-toggle"
                        onclick={toggleFromDropdown}
                        aria-label={showFromDropdown ? 'Hide suggestions' : 'Show suggestions'}
                    >
                        <svg width="8" height="8" viewBox="0 0 8 8" fill="none" aria-hidden="true">
                            <polyline points="1.5,2.5 4,5.5 6.5,2.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                        </svg>
                    </button>
                </div>
                {#if showFromDropdown}
                    <div id="compare-from-listbox" class="commit-dropdown-list" role="listbox" aria-labelledby="compare-from-input">
                        <button
                            id="compare-from-item-current"
                            class="commit-dropdown-item"
                            class:active={fromCommit === CURRENT_TREE}
                            onclick={() => { fromCommit = CURRENT_TREE; fromInputValue = t('git.current'); showFromDropdown = false; fromInputEl?.blur(); }}
                            onkeydown={handleFromKeydown}
                            onfocus={() => handleListItemFocus(-1, 'from')}
                            role="option"
                            aria-selected={fromCommit === CURRENT_TREE}
                        >
                            {#if fromCommit === CURRENT_TREE}
                                <svg width="8" height="8" viewBox="0 0 8 8" fill="none" aria-hidden="true">
                                    <polyline points="1.5,4 3,5.5 6.5,2" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                                </svg>
                            {:else}
                                <span class="commit-check-empty"></span>
                            {/if}
                            <span class="commit-dropdown-hash" style="color: var(--color-status-green)">●</span>
                            <span class="commit-dropdown-subject">{t('git.current')}</span>
                        </button>
                        {@render commitList(filteredFromCommits, fromCommit, 'compare-from', fromInputValue,
                            (commit) => { fromCommit = commit.hash; fromInputValue = commit.short; showFromDropdown = false; fromInputEl?.blur(); },
                            handleFromKeydown, (i) => handleListItemFocus(i, 'from'))}
                    </div>
                {/if}
            </div>

            <svg class="commit-arrow-icon" width="12" height="12" viewBox="0 0 16 16" fill="none" aria-hidden="true" role="presentation">
                <path d="M4 8h8M10 5l3 3-3 3" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
            </svg>

            <div class="commit-selector" class:open={showToDropdown} bind:this={toDropdownEl}>
                <div class="commit-combobox">
                    <svg class="commit-combobox-icon" width="10" height="10" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                        <circle cx="8" cy="8" r="5" stroke="currentColor" stroke-width="1.5"/>
                        <path d="M8 5v6M5 8h6" stroke="currentColor" stroke-width="1.2" stroke-linecap="round"/>
                    </svg>
                    <input
                        id="compare-to-input"
                        bind:this={toInputEl}
                        type="text"
                        class="commit-combobox-input"
                        placeholder="commit hash or subject"
                        value={toInputValue}
                        oninput={handleToInput}
                        onkeydown={handleToKeydown}
                        onblur={handleToInputBlur}
                        onfocus={handleToInputFocus}
                        aria-label="To commit"
                        aria-expanded={showToDropdown}
                        aria-controls="compare-to-listbox"
                        aria-autocomplete="list"
                        role="combobox"
                        autocomplete="off"
                    />
                    <button
                        type="button"
                        class="commit-combobox-toggle"
                        onclick={toggleToDropdown}
                        aria-label={showToDropdown ? 'Hide suggestions' : 'Show suggestions'}
                    >
                        <svg width="8" height="8" viewBox="0 0 8 8" fill="none" aria-hidden="true">
                            <polyline points="1.5,2.5 4,5.5 6.5,2.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                        </svg>
                    </button>
                </div>
                {#if showToDropdown}
                    <div id="compare-to-listbox" class="commit-dropdown-list" role="listbox" aria-labelledby="compare-to-input">
                        {@render commitList(filteredToCommits, toCommit, 'compare-to', toInputValue,
                            (commit) => { toCommit = commit.hash; toInputValue = commit.short; showToDropdown = false; toInputEl?.blur(); },
                            handleToKeydown, (i) => handleListItemFocus(i, 'to'))}
                    </div>
                {/if}
            </div>
        </div>

        <!-- Live region for diff updates (screen reader announcements) -->
        <div class="sr-only" aria-live="polite" aria-atomic="true">
            {#if compareLoading}{t('git.loadingCommitDiff')}{:else if compareError}{t('git.diffError')} {compareError}{:else if compareDiff.length > 0}{t('git.showingDiffWith')} {t('diff.shared.file', { count: compareDiff.length })}{/if}
        </div>

        <!-- Pull / Push / Commit — pinned to the right edge of the header -->
        <div class="panel-actions">
            <button class="panel-icon-btn" onclick={handlePull} disabled={pulling || pushing} title={pulling ? 'Pulling…' : pullLabel} aria-label={pullLabel}>
                {#if pulling}
                    <span class="mini-spinner" aria-hidden="true"></span>
                {:else}
                    <svg width="12" height="12" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                        <path d="M8 3v7M5 7l3 3 3-3" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                        <path d="M3 13h10" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
                    </svg>
                {/if}
                <span class="panel-count" aria-hidden="true">{countMarker}{behindCommits}</span>
            </button>
            <button class="panel-icon-btn" onclick={handlePush} disabled={pulling || pushing} title={pushing ? 'Pushing…' : pushLabel} aria-label={pushLabel}>
                {#if pushing}
                    <span class="mini-spinner" aria-hidden="true"></span>
                {:else}
                    <svg width="12" height="12" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                        <path d="M8 10V3M5 6l3-3 3 3" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                        <path d="M3 13h10" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
                    </svg>
                {/if}
                <span class="panel-count" aria-hidden="true">{countMarker}{aheadCommits}</span>
            </button>
            <button
                class="panel-commit-btn"
                disabled={stagedEntries.length === 0}
                onclick={openCommitDialog}
                title={stagedEntries.length > 0 ? 'Commit staged changes' : 'Nothing staged to commit'}
                aria-label={stagedEntries.length > 0 ? 'Commit staged changes' : 'Nothing staged to commit'}
            >
                <svg width="12" height="12" viewBox="0 0 16 16" fill="none">
                    <circle cx="8" cy="8" r="6" stroke="currentColor" stroke-width="1.2"/>
                    <path d="M5.5 8l2 2 3.5-4" stroke="currentColor" stroke-width="1.2" stroke-linecap="round" stroke-linejoin="round"/>
                </svg>
                <span>{t('git.commit')}</span>
            </button>
        </div>
    </div>

    {#if loading}
        <div class="panel-loading">
            <span class="spinner"></span>
            <span>{t('diff.loadingChanges')}</span>
        </div>
    {:else if !hasContent}
        <div class="panel-empty">
            <svg width="28" height="28" viewBox="0 0 16 16" fill="none">
                <path d="M3 2h6l4 4v8a1 1 0 0 1-1 1H3a1 1 0 0 1-1-1V3a1 1 0 0 1 1-1z" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round" opacity="0.3"/>
                <path d="M9 2v4h4" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round" opacity="0.3"/>
            </svg>
            <p>{t('diff.noChanges')}</p>
        </div>
    {:else}
        <div class="panel-body">
            <!-- Left: Changes file list sidebar -->
            <div class="changes-sidebar" class:hidden={isMobile} style="width: {sidebarWidth}px">
                <!-- svelte-ignore a11y_interactive_supports_focus — its rows are what a keyboard reaches. -->
                <div
                    class="changes-scroll"
                    role="tree"
                    aria-label={t('git.changesListAria')}
                    oncontextmenu={handleListContextmenu}
                    onkeydown={handleListKeydown}
                >
                    {#each groups as group}
                        {#if group.rows.length > 0}
                        <div class="changes-section">
                            <button class="changes-section-header" type="button" onclick={() => toggleSection(group.id)} aria-expanded={!collapsedSections.has(group.id)}>
                                <svg class="changes-chevron" class:expanded={!collapsedSections.has(group.id)} width="10" height="10" viewBox="0 0 16 16" fill="none">
                                    <path d="M6 4l4 4-4 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                                </svg>
                                <span>{group.label}</span>
                                <span class="changes-section-count">{group.rows.length}</span>
                                <span class="changes-section-action" onclick={(e) => { e.stopPropagation(); group.run(); }} onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); e.stopPropagation(); group.run(); } }} title={group.allLabel} role="button" tabindex="0" aria-label={group.allLabel}>
                                    {#if group.id === 'staged'}
                                        <svg width="10" height="10" viewBox="0 0 16 16" fill="none" aria-hidden="true"><path d="M4 8h8" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>
                                    {:else}
                                        <svg width="10" height="10" viewBox="0 0 16 16" fill="none" aria-hidden="true"><path d="M8 4v8M4 8h8" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>
                                    {/if}
                                </span>
                            </button>
                            {#if !collapsedSections.has(group.id)}
                            <div class="changes-section-body" role="group" aria-label={group.label}>
                                {#each group.rows as row}
                                    {@const { folder, name } = splitPath(row.path)}
                                    {@const indexed = row.group === 'staged'}
                                    <div class="changes-file-row">
                                        <button
                                            class="changes-file-item"
                                            class:selected={selectedPath === row.path || selection.isSelected(row.path)}
                                            role="treeitem"
                                            aria-selected={selection.isSelected(row.path)}
                                            data-index={row.index}
                                            onclick={(e) => handleRowClick(e, row)}
                                            oncontextmenu={(e) => openMenuAt(e, e.clientX, e.clientY, row)}
                                            onpointerdown={(e) => selection.handlePointerDown(e, row.index)}
                                            onpointermove={selection.handlePointerMove}
                                            onpointerup={selection.cancelLongPress}
                                            onpointercancel={selection.cancelLongPress}
                                            title={row.path}
                                        >
                                            <span class="changes-file-basename">{name}</span>
                                            {#if folder}<span class="changes-file-folder">{folder}</span>{/if}
                                            <span class="changes-file-badge" style="color: {getStatusBadge(row).color}" title={gitStatusLabel(row.status)}>
                                                {row.status === '?' ? '??' : row.status}
                                            </span>
                                        </button>
                                        <button
                                            class="changes-stage-btn"
                                            type="button"
                                            onclick={() => handleStagePaths([row.path], !indexed)}
                                            title={t(indexed ? 'git.unstageFile' : 'git.stageFile', { path: row.path })}
                                            aria-label={t(indexed ? 'git.unstageFile' : 'git.stageFile', { path: row.path })}
                                        >
                                            <svg width="10" height="10" viewBox="0 0 16 16" fill="none">
                                                {#if indexed}
                                                    <path d="M4 8h8" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
                                                {:else}
                                                    <path d="M8 4v8M4 8h8" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
                                                {/if}
                                            </svg>
                                        </button>
                                        <button
                                            class="changes-discard-btn"
                                            type="button"
                                            onclick={() => handleDiscardRows([row])}
                                            title={t('git.discardFile', { path: row.path })}
                                            aria-label={t('git.discardFile', { path: row.path })}
                                        >
                                            <svg width="10" height="10" viewBox="0 0 16 16" fill="none" aria-hidden="true"><path d="M3 4.5h10M6.5 4.5V3h3v1.5M4.5 4.5l.6 8.5h5.8l.6-8.5" stroke="currentColor" stroke-width="1.2" stroke-linecap="round" stroke-linejoin="round"/></svg>
                                        </button>
                                    </div>
                                {/each}
                            </div>
                            {/if}
                        </div>
                        {/if}
                    {/each}
                </div>

                {#if selection.showActionBar}
                    <SelectionActionBar count={selectedRows.length} onClear={selection.clear}>
                        {#snippet actions()}
                            <button
                                type="button"
                                class="selection-btn"
                                disabled={selectedBySection.unstaged.length === 0}
                                title={t('git.stage')}
                                aria-label={t('git.stage')}
                                onclick={() => handleStagePaths(selectedBySection.unstaged, true)}
                            >
                                <Icon src={stageIcon} width={14} height={14} />
                            </button>
                            <button
                                type="button"
                                class="selection-btn"
                                disabled={selectedBySection.staged.length === 0}
                                title={t('git.unstage')}
                                aria-label={t('git.unstage')}
                                onclick={() => handleStagePaths(selectedBySection.staged, false)}
                            >
                                <Icon src={unstageIcon} width={14} height={14} />
                            </button>
                            <button
                                type="button"
                                class="selection-btn destructive"
                                title={t('explorer.shared.discard')}
                                aria-label={t('explorer.shared.discard')}
                                onclick={() => handleDiscardRows(selectedRows)}
                            >
                                <Icon src={discardIcon} width={14} height={14} />
                            </button>
                        {/snippet}
                    </SelectionActionBar>
                {/if}

                {#if menu}
                    <FileContextMenu
                        x={menu.x}
                        y={menu.y}
                        items={menuItems}
                        ariaLabel={t('explorer.shared.menuAria')}
                        onClose={() => menu = null}
                    />
                {/if}
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
                {#if fromCommit !== CURRENT_TREE}
                    <!-- Commit comparison diff -->
                    {#if diffViewer.component}
                        {@const DiffViewer = diffViewer.component}
                        <DiffViewer
                            committedFiles={compareDiff}
                            stagedFiles={[]}
                            unstagedFiles={[]}
                            loading={compareLoading}
                        />
                    {:else}
                        <div class="diff-viewer-loading">{t('git.loadingDiffViewer')}</div>
                    {/if}
                    {#if compareError}
                        <div class="compare-error" role="alert">{compareError}</div>
                    {/if}
                {:else if stagedEntries.length > 0 || unstagedEntries.length > 0 || unstagedUntracked.length > 0 || fromCommit === CURRENT_TREE}
                    {#if diffViewer.component}
                        {@const DiffViewer = diffViewer.component}
                        <DiffViewer
                            stagedFiles={sortedStagedFiles}
                            unstagedFiles={sortedUnstagedFiles}
                            unstagedUntracked={unstagedUntracked}
                            selectedPath={selectedPath}
                            onStageFile={(path, stage) => handleStagePaths([path], stage)}
                            onStageAll={() => handleStagePaths(pathsOf('unstaged'), true)}
                            onUnstageAll={() => handleStagePaths(pathsOf('staged'), false)}
                            onExpandUntracked={loadUntrackedFile}
                            onLineClick={handleLineClick}
                        />
                    {:else}
                        <div class="diff-viewer-loading">{t('git.loadingDiffViewer')}</div>
                    {/if}
                {/if}
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
        flex-wrap: wrap;
    }
    .panel-title {
        font-size: 0.75rem;
        font-weight: 600;
        color: var(--color-text-secondary);
        text-transform: uppercase;
        letter-spacing: 0.05em;
        flex-shrink: 0;
    }
    .close-btn {
        padding: 4px;
        border: none;
        background: transparent;
        color: var(--color-text-tertiary);
        cursor: pointer;
        border-radius: 4px;
        display: flex;
        align-items: center;
        justify-content: center;
        flex-shrink: 0;
    }
    .close-btn:hover {
        color: var(--color-text-primary);
        background: var(--color-surface-3);
    }
    .close-btn:focus-visible {
        outline: 2px solid var(--color-accent);
        outline-offset: -1px;
    }

    /* Commit comparison selectors */
    .commit-selectors {
        display: flex;
        align-items: center;
        gap: 4px;
        flex-shrink: 0;
    }
    /* Pull / Push / Commit — always pinned to the right end of the header row */
    .panel-actions {
        display: flex;
        align-items: center;
        gap: 6px;
        margin-left: auto;
        flex-shrink: 0;
    }
    .commit-arrow-icon {
        color: var(--color-text-tertiary);
        flex-shrink: 0;
    }
    .commit-selector {
        position: relative;
    }
    /* Combobox */
    .commit-combobox {
        display: flex;
        align-items: center;
        gap: 3px;
        padding: 1px 4px;
        border: 1px solid var(--color-border);
        background: var(--color-surface-1);
        border-radius: 4px;
        max-width: 220px;
    }
    .commit-selector.open .commit-combobox-input {
        border-bottom-left-radius: 0;
        border-bottom-right-radius: 0;
    }
    .commit-combobox-icon {
        color: var(--color-text-tertiary);
        flex-shrink: 0;
    }
    .commit-combobox-input {
        flex: 1;
        min-width: 0;
        padding: 2px 4px;
        border: none;
        background: transparent;
        color: var(--color-text-secondary);
        font-size: 0.75rem;
        font-family: var(--font-mono);
        outline: none;
        width: 100%;
    }
    .commit-combobox-input::placeholder {
        color: var(--color-text-tertiary);
        font-style: italic;
    }
    .commit-combobox-toggle {
        display: flex;
        align-items: center;
        justify-content: center;
        padding: 2px;
        border: none;
        background: transparent;
        color: var(--color-text-tertiary);
        cursor: pointer;
        border-radius: 3px;
        flex-shrink: 0;
    }
    .commit-combobox-toggle:hover {
        color: var(--color-text-primary);
        background: var(--color-surface-3);
    }
    .commit-combobox-toggle:focus-visible {
        outline: 2px solid var(--color-accent);
        outline-offset: -1px;
    }
    .commit-dropdown-empty {
        padding: 6px 10px;
        color: var(--color-text-tertiary);
        font-size: 0.75rem;
        font-style: italic;
    }
    .commit-dropdown-list {
        position: absolute;
        top: 100%;
        left: 0;
        z-index: 100;
        min-width: 260px;
        max-height: 240px;
        overflow-y: auto;
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        box-shadow: 0 4px 12px rgba(0, 0, 0, 0.3);
        margin-top: 2px;
    }
    .commit-dropdown-item {
        display: flex;
        align-items: center;
        gap: 6px;
        width: 100%;
        padding: 5px 8px;
        border: none;
        background: transparent;
        color: var(--color-text-primary);
        font-size: 0.75rem;
        text-align: left;
        cursor: pointer;
    }
    .commit-dropdown-item:hover {
        background: var(--color-surface-3);
    }
    .commit-dropdown-item:focus-visible {
        outline: 2px solid var(--color-accent);
        outline-offset: -2px;
        background: var(--color-surface-3);
    }
    .commit-dropdown-item.active {
        color: var(--color-accent-light);
        font-weight: 600;
    }
    .commit-check-empty {
        display: inline-block;
        width: 8px;
    }
    .commit-dropdown-hash {
        font-family: var(--font-mono);
        font-size: 0.75rem;
        color: var(--color-text-secondary);
        flex-shrink: 0;
        width: 3.5rem;
    }
    .commit-dropdown-subject {
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
        flex: 1;
        min-width: 0;
    }

    /* Compare error */
    .compare-error {
        padding: 8px 12px;
        color: var(--color-status-red);
        font-size: 0.75rem;
        background: rgba(248, 81, 73, 0.1);
        border-top: 1px solid var(--color-border);
        display: flex;
        align-items: center;
        gap: 6px;
    }
    /* Screen reader only (visually hidden) */
    .panel-icon-btn {
        display: flex;
        align-items: center;
        justify-content: center;
        gap: 3px;
        min-width: 22px;
        height: 22px;
        padding: 0 4px;
        border: none;
        background: transparent;
        color: var(--color-text-secondary);
        cursor: pointer;
        border-radius: 4px;
        flex-shrink: 0;
    }
    .panel-icon-btn:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
    .panel-icon-btn:disabled,
    .panel-icon-btn:disabled:hover {
        opacity: 0.5;
        cursor: not-allowed;
        background: transparent;
        color: var(--color-text-secondary);
    }

    /* Commit button in header */
    .panel-commit-btn {
        display: inline-flex;
        align-items: center;
        gap: 4px;
        padding: 3px 8px;
        border: none;
        background: var(--color-accent-dark);
        color: white;
        font-size: 0.75rem;
        font-weight: 500;
        cursor: pointer;
        border-radius: 4px;
        white-space: nowrap;
    }
    .panel-commit-btn:hover {
        opacity: 0.9;
    }
    .panel-commit-btn:disabled,
    .panel-commit-btn:disabled:hover {
        opacity: 0.45;
        cursor: not-allowed;
    }

    /* Busy indicator for Pull/Push */
    .mini-spinner {
        width: 12px;
        height: 12px;
        border: 2px solid currentColor;
        border-top-color: transparent;
        border-radius: 50%;
        animation: spin 0.6s linear infinite;
    }

    .panel-count {
        font-family: var(--font-mono);
        font-size: 0.6875rem;
        line-height: 1;
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
        /* The selection bar is placed against this box. */
        position: relative;
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
        font-size: 0.75rem;
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
        font-size: 0.75rem;
        font-weight: 500;
        color: var(--color-text-tertiary);
    }
    .changes-section-action {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        width: 18px;
        height: 18px;
        padding: 0;
        border: none;
        background: transparent;
        color: var(--color-text-tertiary);
        cursor: pointer;
        border-radius: 3px;
        flex-shrink: 0;
    }
    .changes-section-action:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
    .changes-section-action:focus-visible {
        outline: 1px solid var(--color-accent);
        outline-offset: 1px;
    }
    .changes-section-body {
        padding: 0 4px;
    }

    /* File row: item + stage button */
    .changes-file-row {
        display: flex;
        align-items: center;
        gap: 2px;
    }

    /* File items */
    .changes-file-item {
        display: flex;
        align-items: center;
        gap: 6px;
        flex: 1;
        min-width: 0;
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
        font-size: 0.75rem;
        font-weight: 700;
        font-family: var(--font-mono);
        flex-shrink: 0;
        width: 1.5rem;
        text-align: center;
        user-select: none;
    }

    /* Stage/unstage button in sidebar */
    .changes-stage-btn,
    .changes-discard-btn {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 20px;
        height: 20px;
        padding: 0;
        border: none;
        background: transparent;
        color: var(--color-text-tertiary);
        cursor: pointer;
        border-radius: 3px;
        flex-shrink: 0;
        opacity: 0;
        transition: opacity 0.1s ease;
    }
    .changes-file-row:hover .changes-stage-btn,
    .changes-file-row:hover .changes-discard-btn,
    .changes-stage-btn:focus-visible,
    .changes-discard-btn:focus-visible {
        opacity: 1;
    }
    .changes-stage-btn:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
    .changes-discard-btn:hover {
        background: var(--color-surface-3);
        color: var(--color-status-red);
    }

    /* Diff viewer area */
    .changes-diff {
        flex: 1;
        min-width: 0;
        display: flex;
        flex-direction: column;
        overflow: hidden;
    }

    .diff-viewer-loading {
        display: flex;
        align-items: center;
        justify-content: center;
        padding: 48px 24px;
        color: var(--color-text-tertiary);
        font-size: 0.8125rem;
        flex: 1;
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
        /* Larger touch targets on mobile */
        .panel-icon-btn {
            width: 44px;
            height: 44px;
        }
        .panel-commit-btn {
            padding: 8px 14px;
            min-height: 44px;
        }
        /* Header: the actions share the "Changes" row (right-aligned), the
           commit comparison selectors drop to their own full-width row. */
        .panel-actions {
            order: 1;
        }
        .commit-selectors {
            order: 2;
            flex-basis: 100%;
        }
        .commit-selector {
            flex: 1;
            min-width: 0;
        }
    }
</style>
