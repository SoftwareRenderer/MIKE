<script lang="ts">
    import { onMount } from 'svelte';
    import { getGitStatus } from '$lib/api';
    import { gitStatusLabel, gitStatusClass, gitStatusPriority } from '$lib/utils/git-status';
    import FileBrowserView from '$lib/components/FileBrowserView.svelte';
    import { t } from '$lib/i18n';

    let { projectId, projectPath, initialPath, sandboxEnabled, unsavedFiles, pendingFsEvent, onFileSelected, onPathChange, onToggleFind, onDeleted }: {
        projectId: string;
        projectPath: string;
        initialPath?: string;
        sandboxEnabled: boolean;
        unsavedFiles: string[];
        pendingFsEvent: { op: 'create' | 'delete'; path: string } | null;
        onFileSelected: (path: string, line?: number, pin?: boolean) => void;
        onPathChange: (path: string) => void;
        onToggleFind?: () => void;
        onDeleted?: (paths: string[]) => void;
    } = $props();

    // Sandbox mount path for this project
    let mountPath = $derived(sandboxEnabled ? '/home/sandbox/' + projectPath.split('/').filter(Boolean).pop() : '');
    // Everything listed, written and dropped lands under this: the mount when sandboxed, the checkout otherwise.
    let rootPath = $derived(sandboxEnabled ? mountPath : projectPath);
    // The explorer remembers the folder it was on; a sandboxed project opens at its mount.
    let startPath = $derived(sandboxEnabled ? mountPath : (initialPath || projectPath));

    // Git status map: absolute_path -> single-char status (M, A, D, ?, etc.)
    let gitStatusMap = $state(new Map<string, string>());

    /** Fetch and parse git status into a Map<absolute_path, status_char>. */
    async function loadGitStatus() {
        try {
            const { status } = await getGitStatus(projectId);
            const map = new Map<string, string>();
            const lines = status.split('\n').filter(l => l.length > 0);
            for (const line of lines) {
                // Porcelain format: "XY path" where X=index, Y=working tree
                // Renamed: "XY path -> new_path"
                const statusChars = line.substring(0, 2);
                const pathPart = line.substring(3);
                // Use working tree status (pos 1), fall back to index (pos 0)
                const statusChar = statusChars[1] !== ' ' && statusChars[1] !== '?' ? statusChars[1] : statusChars[0];
                if (statusChar === ' ') continue; // no change
                // Handle renames: "R  old -> new" — map both paths
                const arrowIdx = pathPart.indexOf(' -> ');
                if (arrowIdx >= 0) {
                    map.set(pathPart.substring(0, arrowIdx), statusChar);
                    map.set(pathPart.substring(arrowIdx + 4), statusChar);
                } else {
                    map.set(pathPart, statusChar);
                }
            }
            // Convert relative paths (from git root) to absolute paths
            const absMap = new Map<string, string>();
            for (const [relPath, char] of map) {
                const absPath = rootPath + '/' + relPath;
                absMap.set(absPath, char);
                // Propagate status to all parent directories
                let dir = absPath.substring(0, absPath.lastIndexOf('/'));
                while (dir.length > rootPath.length) {
                    const existing = absMap.get(dir);
                    if (!existing || gitStatusPriority(char) > gitStatusPriority(existing)) {
                        absMap.set(dir, char);
                    }
                    dir = dir.substring(0, dir.lastIndexOf('/'));
                }
            }
            gitStatusMap = absMap;
        } catch (e) {
            console.error('Failed to fetch git status:', e);
        }
    }

    /** The one thing this browser shows that the listing does not know about: git's letter. */
    function gitBadge(entry: { name: string; isDir: boolean; path: string }) {
        const char = gitStatusMap.get(entry.path) || '';
        if (!char) return null;
        return {
            char,
            cls: gitStatusClass(char),
            title: t('explorer.branchFiles.gitStatusTitle', { status: char }),
            ariaLabel: t('explorer.branchFiles.gitStatusAria', { status: gitStatusLabel(char) }),
        };
    }

    // Any change in the repo can move a status, whatever the listing made of the event.
    onMount(() => {
        loadGitStatus();
    });
</script>

<FileBrowserView
    {projectId}
    {rootPath}
    initialPath={startPath}
    navMode="path"
    pinOnDblclick
    {unsavedFiles}
    {pendingFsEvent}
    onFsEvent={loadGitStatus}
    onMutated={loadGitStatus}
    {onDeleted}
    extraEntryInfo={gitBadge}
    emptyMessage={t('explorer.branchFiles.empty')}
    pendingMessage={t('explorer.branchFiles.initSandbox')}
    treeAriaLabel={t('explorer.branchFiles.treeAria')}
    {onFileSelected}
    {onPathChange}
    {onToggleFind}
/>
