<script lang="ts">
    let { onClose, onSelect }: { onClose: () => void; onSelect: (path: string) => void } = $props();

    let currentPath = $state('');
    let entries = $state<{ name: string; isDir: boolean; path: string; isGitRepo: boolean }[]>([]);
    let isGitRepo = $state(false);
    let loading = $state(false);
    let error = $state('');
    let inputPath = $state('');
    let showInput = $state(false);

    async function loadDir(path: string) {
        loading = true;
        error = '';
        currentPath = path;
        try {
            const res = await fetch(`/api/fs?path=${encodeURIComponent(path)}`);
            if (!res.ok) {
                const err = await res.json();
                throw new Error(err.error || 'Failed to read directory');
            }
            const data = await res.json();
            entries = data.entries || [];
            isGitRepo = data.isGitRepo || false;
        } catch (e) {
            error = String(e);
            entries = [];
        } finally {
            loading = false;
        }
    }

    function handleClick(entry: { isDir: boolean; path: string; isGitRepo: boolean }) {
        if (entry.isGitRepo) {
            onSelect(entry.path);
        } else if (entry.isDir) {
            loadDir(entry.path);
        }
    }

    function handleSelect() {
        if (isGitRepo) {
            onSelect(currentPath);
        }
    }

    function handleInputChange() {
        const p = inputPath.trim();
        if (p) {
            loadDir(p);
            showInput = false;
        }
    }

    function toggleInput() {
        if (!showInput) {
            inputPath = currentPath;
        }
        showInput = !showInput;
    }

    // Load home directory on mount
    $effect(() => {
        loadDir('/home');
    });

    function formatPath(p: string): string {
        return p || '/';
    }
</script>

<div class="overlay" onclick={onClose}>
    <div class="browser" onclick={(e) => e.stopPropagation()}>
        <div class="browser-header">
            <h2>Select Project Directory</h2>
            <button class="close-btn" onclick={onClose}>✕</button>
        </div>

        <div class="browser-body">
            <!-- Path bar -->
            <div class="path-bar">
                <button class="home-btn" onclick={() => loadDir('/home')} title="Home">🏠</button>
                <input
                    type="text"
                    class="path-input"
                    value={currentPath}
                    onfocus={() => { inputPath = currentPath; showInput = true; }}
                    onblur={() => { if (!inputPath.trim()) showInput = false; }}
                    onkeydown={(e) => { if (e.key === 'Enter') handleInputChange(); }}
                    placeholder="Enter path or browse..."
                />
            </div>

            {#if error}
                <div class="error">{error}</div>
            {/if}

            {#if loading}
                <div class="loading">Loading...</div>
            {:else}
                <!-- Git indicator -->
                {#if isGitRepo}
                    <div class="git-indicator">✓ This is a git repository</div>
                {/if}

                <!-- File list -->
                <div class="file-list">
                    {#each entries as entry}
                        <div
                            class={`file-entry${entry.isDir ? ' dir' : ''}${entry.isGitRepo ? ' git-repo' : ''}`}
                            onclick={() => handleClick(entry)}
                        >
                            <span class="icon">{entry.isDir ? '📁' : '📄'}</span>
                            <span class="name">{entry.name}</span>
                            {#if entry.isGitRepo}
                                <span class="git-badge">git</span>
                            {/if}
                        </div>
                    {/each}
                </div>
            {/if}
        </div>

        <div class="browser-footer">
            <span class="current-path">{formatPath(currentPath)}</span>
            <div class="footer-actions">
                <button class="btn-cancel" onclick={onClose}>Cancel</button>
                <button
                    class="btn-select"
                    onclick={handleSelect}
                    disabled={!isGitRepo}
                >
                    Select
                </button>
            </div>
        </div>
    </div>
</div>

<style>
    .overlay {
        position: fixed;
        inset: 0;
        background: rgba(0, 0, 0, 0.6);
        display: flex;
        align-items: center;
        justify-content: center;
        z-index: 300;
    }
    .browser {
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-lg);
        width: 560px;
        max-width: 90vw;
        max-height: 80vh;
        display: flex;
        flex-direction: column;
        overflow: hidden;
    }
    .browser-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 16px 20px;
        border-bottom: 1px solid var(--color-border);
    }
    .browser-header h2 {
        font-size: 16px;
        font-weight: 600;
    }
    .close-btn {
        font-size: 18px;
        color: var(--color-text-secondary);
        padding: 4px;
    }
    .browser-body {
        flex: 1;
        overflow-y: auto;
        padding: 16px;
        display: flex;
        flex-direction: column;
        gap: 12px;
    }
    .path-bar {
        display: flex;
        gap: 8px;
        align-items: center;
    }
    .home-btn {
        font-size: 18px;
        padding: 4px 8px;
        background: var(--color-surface-3);
        border-radius: var(--radius-sm);
    }
    .path-input {
        flex: 1;
        padding: 8px 12px;
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        color: var(--color-text-primary);
        font-size: 13px;
        font-family: var(--font-mono);
        outline: none;
    }
    .path-input:focus {
        border-color: var(--color-accent);
    }
    .error {
        color: var(--color-status-red);
        font-size: 13px;
        padding: 8px;
        background: var(--color-status-red)11;
        border-radius: var(--radius-sm);
    }
    .loading {
        text-align: center;
        color: var(--color-text-secondary);
        padding: 24px;
    }
    .git-indicator {
        color: var(--color-status-green);
        font-size: 13px;
        padding: 8px;
        background: var(--color-status-green)11;
        border-radius: var(--radius-sm);
        text-align: center;
    }
    .file-list {
        display: flex;
        flex-direction: column;
        gap: 2px;
    }
    .file-entry {
        display: flex;
        align-items: center;
        gap: 8px;
        padding: 8px 12px;
        border-radius: var(--radius-sm);
        cursor: pointer;
        font-size: 14px;
    }
    .file-entry:hover {
        background: var(--color-surface-2);
    }
    .file-entry.dir {
        font-weight: 500;
    }
    .file-entry.git-repo:hover {
        background: var(--color-status-green)22;
    }
    .icon {
        font-size: 16px;
        flex-shrink: 0;
    }
    .name {
        flex: 1;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }
    .git-badge {
        font-size: 10px;
        padding: 1px 6px;
        background: var(--color-status-green)33;
        color: var(--color-status-green);
        border-radius: 10px;
        font-weight: 600;
    }
    .browser-footer {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 12px 16px;
        border-top: 1px solid var(--color-border);
        gap: 12px;
    }
    .current-path {
        font-size: 12px;
        color: var(--color-text-tertiary);
        font-family: var(--font-mono);
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
        flex: 1;
    }
    .footer-actions {
        display: flex;
        gap: 8px;
    }
    .btn-cancel {
        padding: 6px 16px;
        background: var(--color-surface-3);
        border-radius: var(--radius-md);
        font-size: 13px;
    }
    .btn-cancel:hover {
        background: var(--color-surface-4);
    }
    .btn-select {
        padding: 6px 16px;
        background: var(--color-accent);
        color: white;
        border-radius: var(--radius-md);
        font-size: 13px;
        font-weight: 500;
    }
    .btn-select:hover:not(:disabled) {
        background: var(--color-accent-hover);
    }
    .btn-select:disabled {
        opacity: 0.4;
        cursor: not-allowed;
    }
</style>
