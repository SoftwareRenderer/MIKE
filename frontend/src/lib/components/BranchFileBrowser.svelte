<script lang="ts">
    import folderIcon from '$lib/assets/icons/folder.svg';
    import fileIcon from '$lib/assets/icons/file.svg';

    let { projectPath, unsavedFiles, onFileSelected }: {
        projectPath: string;
        unsavedFiles: string[];
        onFileSelected: (path: string) => void;
    } = $props();

    let currentPath = $state(projectPath);
    let entries = $state<{ name: string; isDir: boolean; path: string }[]>([]);
    let loading = $state(true);
    let error = $state('');

    // Directories to never show (from backend ignoredDirs)
    const ignoredDirs = new Set([
        'node_modules', '.next', 'dist', 'build', '.cache', '__pycache__',
        '.venv', 'target', 'vendor', '.gradle', '.mypy_cache', '.pytest_cache',
        'coverage', '.nuxt', '.output', '.svelte-kit', '.turbo', '.vercel',
        '.docker', '.git', '.vscode', '.idea',
    ]);

    async function loadDir(path: string) {
        loading = true;
        error = '';
        currentPath = path;
        try {
            const res = await fetch(`/api/fs?path=${encodeURIComponent(path)}&files=1`);
            if (!res.ok) {
                const err = await res.json().catch(() => ({}));
                throw new Error(err.error || 'Failed to read directory');
            }
            const data = await res.json();
            entries = (data.entries || [])
                .filter((e: any) => {
                    // Keep ".." for navigation, filter out other dotfiles and ignored directories
                    if (e.name === '..') return true;
                    if (e.name.startsWith('.')) return false;
                    if (e.isDir && ignoredDirs.has(e.name)) return false;
                    return true;
                })
                .sort((a: any, b: any) => {
                    // Directories first, then files, alphabetical within each group
                    if (a.isDir !== b.isDir) return a.isDir ? -1 : 1;
                    return a.name.localeCompare(b.name);
                });
        } catch (e) {
            error = String(e);
            entries = [];
        } finally {
            loading = false;
        }
    }

    function handleClick(entry: { isDir: boolean; path: string }) {
        if (entry.isDir) {
            loadDir(entry.path);
        } else {
            onFileSelected(entry.path);
        }
    }

    function handlePathInput() {
        const p = currentPath.trim();
        if (p) {
            loadDir(p);
        }
    }

    $effect(() => {
        loadDir(projectPath);
    });
</script>

<div class="file-browser">
    <div class="file-browser-header">
        <span class="file-browser-label">Files</span>
    </div>

    {#if error}
        <div class="file-browser-error">{error}</div>
    {:else if loading && entries.length === 0}
        <div class="file-browser-loading">Loading...</div>
    {:else}
        <div class="file-browser-path">
            <input
                type="text"
                class="file-browser-path-input"
                value={currentPath}
                oninput={(e) => currentPath = e.target.value}
                onkeydown={(e) => e.key === 'Enter' && handlePathInput()}
                onblur={handlePathInput}
                title={currentPath}
            />
        </div>
        <div class="file-browser-list" role="tree" aria-label="File browser">
            {#each entries as entry}
                <button
                    class="file-browser-item"
                    class:dir={entry.isDir}
                    onclick={() => handleClick(entry)}
                    role="treeitem"
                    title={entry.path}
                >
                    {#if entry.isDir}
                        <img src={folderIcon} width="14" height="14" alt="" class="file-icon" />
                    {:else}
                        <img src={fileIcon} width="14" height="14" alt="" class="file-icon" />
                    {/if}
                    <span class="file-name">{entry.name}</span>
                    {#if !entry.isDir && unsavedFiles.includes(entry.path)}
                        <span class="unsaved-dot" title="Unsaved changes">
                            <svg width="8" height="8" viewBox="0 0 8 8" aria-hidden="true">
                                <circle cx="4" cy="4" r="3.5" fill="currentColor"/>
                            </svg>
                        </span>
                    {/if}
                </button>
            {/each}
            {#if entries.length === 0 && !loading}
                <div class="file-browser-empty">Empty directory</div>
            {/if}
        </div>
    {/if}
</div>

<style>
    .file-browser {
        display: flex;
        flex-direction: column;
        height: 100%;
        overflow: hidden;
    }

    .file-browser-header {
        padding: 6px 10px;
        border-bottom: 1px solid var(--color-border);
        flex-shrink: 0;
    }

    .file-browser-label {
        font-size: 0.6875rem;
        font-weight: 600;
        color: var(--color-text-secondary);
        text-transform: uppercase;
        letter-spacing: 0.05em;
    }

    .file-browser-error {
        padding: 8px 10px;
        color: var(--color-status-red);
        font-size: 0.75rem;
    }

    .file-browser-loading {
        padding: 8px 10px;
        color: var(--color-text-secondary);
        font-size: 0.75rem;
    }

    .file-browser-path {
        padding: 4px 6px;
        border-bottom: 1px solid var(--color-border);
        flex-shrink: 0;
    }

    .file-browser-path-input {
        width: 100%;
        padding: 2px 4px;
        font-size: 0.6875rem;
        font-family: var(--font-mono);
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        color: var(--color-text-secondary);
        outline: none;
    }
    .file-browser-path-input:focus {
        border-color: var(--color-accent);
        color: var(--color-text-primary);
    }

    .file-browser-list {
        flex: 1;
        overflow-y: auto;
        overflow-x: hidden;
        padding: 2px 0;
    }

    .file-browser-item {
        display: flex;
        align-items: center;
        gap: 6px;
        width: 100%;
        padding: 3px 8px 3px 10px;
        border: none;
        background: transparent;
        color: var(--color-text-primary);
        font-size: 0.75rem;
        text-align: left;
        cursor: pointer;
        border-radius: 0;
    }
    .file-browser-item:hover {
        background: var(--color-surface-2);
        opacity: 1;
    }
    .file-browser-item.dir {
        font-weight: 500;
    }

    .file-icon {
        flex-shrink: 0;
        opacity: 0.7;
    }

    .file-name {
        flex: 1;
        min-width: 0;
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
    }

    .unsaved-dot {
        flex-shrink: 0;
        color: var(--color-status-yellow, #f0c000);
        display: inline-flex;
        align-items: center;
        margin-left: 2px;
    }

    .file-browser-empty {
        padding: 8px 10px;
        color: var(--color-text-tertiary);
        font-size: 0.75rem;
    }
</style>
