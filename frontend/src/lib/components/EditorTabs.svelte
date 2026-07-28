<script lang="ts">
    import FileEditor from './FileEditor.svelte';

    interface OpenFile {
        path: string;
        targetLine: number;
    }

    let { projectId, projectPath, taskId, openFiles, activeFilePath, unsavedFiles, pinnedFiles, onFocused, onSymbolsUpdate, onHasChanges,
        onSelectTab, onCloseTab, onPinTab, onSave, saving, onToggleFullscreen, mobileFullscreen }: {
        projectId: string;
        projectPath?: string;
        taskId?: string;
        openFiles: OpenFile[];
        activeFilePath: string;
        unsavedFiles: Set<string>;
        pinnedFiles: Set<string>;
        onFocused: (focused: boolean) => void;
        onSymbolsUpdate: (symbols: Array<{ name: string; kind: string; line: number; indent: number }>) => void;
        onHasChanges: (filePath: string, hasChanges: boolean) => void;
        onSelectTab: (path: string) => void;
        onCloseTab: (path: string) => void | Promise<void>;
        onPinTab: (path: string) => void;
        onSave?: () => void;
        saving?: boolean;
        onToggleFullscreen?: () => void;
        mobileFullscreen?: boolean;
    } = $props();

    // Lazy-loaded on first file edit
    let FileEditorComponent: typeof FileEditor | null = $state(null);
    let fileEditorLoading = $state(false);

    function loadFileEditor(): void {
        if (FileEditorComponent || fileEditorLoading) return;
        fileEditorLoading = true;
        import('./FileEditor.svelte').then((mod) => {
            FileEditorComponent = mod.default;
            fileEditorLoading = false;
        }).catch(() => { fileEditorLoading = false; });
    }

    $effect(() => {
        if (openFiles.length > 0) {
            loadFileEditor();
        }
    });

    function getFileName(path: string): string {
        return path.split('/').pop() || path;
    }

    function handleEditorFocused(filePath: string, focused: boolean) {
        if (filePath === activeFilePath) {
            onFocused(focused);
        }
    }

    function handleSymbolsUpdate(filePath: string, symbols: Array<{ name: string; kind: string; line: number; indent: number }>) {
        if (filePath === activeFilePath) {
            onSymbolsUpdate(symbols);
        }
    }

    function handleHasChanges(filePath: string, hasChanges: boolean) {
        onHasChanges(filePath, hasChanges);
    }
</script>

<div class="editor-tabs-container">
    <!-- Tab bar -->
    <div class="editor-tab-bar" role="tablist" aria-label="Open files">
        {#each openFiles as file (file.path)}
            <div
                class="editor-tab"
                class:active={file.path === activeFilePath}
                class:unsaved={unsavedFiles.has(file.path)}
                role="tab"
                tabindex="0"
                aria-selected={file.path === activeFilePath}
                aria-controls="editor-panel-{file.path.replace(/[^a-zA-Z0-9]/g, '-')}"
                onclick={() => onSelectTab(file.path)}
                onmousedown={(e) => { if (e.button === 1) { e.preventDefault(); onCloseTab(file.path); } }}
                ondblclick={() => { if (!pinnedFiles.has(file.path)) onPinTab(file.path); }}
                onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onSelectTab(file.path); } }}
            >
                <span class="editor-tab-icon">
                    <svg width="12" height="12" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                        <path d="M3 2h6l4 4v8a1 1 0 0 1-1 1H3a1 1 0 0 1-1-1V3a1 1 0 0 1 1-1z" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round"/>
                        <path d="M9 2v4h4" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round"/>
                    </svg>
                </span>
                <span class="editor-tab-name" class:preview={!pinnedFiles.has(file.path)} title={file.path}>{getFileName(file.path)}</span>
                <button
                    class="btn-close-tab"
                    onclick={(e) => { e.stopPropagation(); onCloseTab(file.path); }}
                    ondblclick={(e) => { e.stopPropagation(); if (!pinnedFiles.has(file.path)) onPinTab(file.path); }}
                    aria-label={`Close {getFileName(file.path)}`}
                    title="Close file"
                >
                    {#if unsavedFiles.has(file.path)}
                        <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                            <circle cx="8" cy="8" r="5" fill="currentColor"/>
                        </svg>
                    {:else}
                        <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                            <path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
                        </svg>
                    {/if}
                </button>
            </div>
        {/each}

        <!-- Actions -->
        {#if openFiles.length > 0}
            <div class="editor-tab-actions">
                {#if onToggleFullscreen}
                    <button type="button" class="editor-icon-btn" onclick={onToggleFullscreen} title="Toggle fullscreen">
                        {#if mobileFullscreen}
                            <svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M10 2V6H14M6 14V10H2" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
                        {:else}
                            <svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M10 2h4v4M6 14H2v-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
                        {/if}
                    </button>
                {/if}
                {#if onSave}
                    <button
                        type="button"
                        class="editor-save-btn"
                        disabled={!unsavedFiles.has(activeFilePath) || saving}
                        onclick={onSave}
                        title="Save (Ctrl+S)"
                    >
                        {saving ? 'Saving...' : 'Save'}
                    </button>
                {/if}
            </div>
        {/if}
    </div>

    <!-- Editor panels -->
    <div class="editor-panels">
        {#if fileEditorLoading}
            <div class="file-editor-loading">Loading editor...</div>
        {:else if FileEditorComponent}
            {@const FileEditor = FileEditorComponent}
            {#each openFiles as file (file.path)}
                <FileEditor
                    projectId={projectId}
                    projectPath={projectPath}
                    taskId={taskId}
                    filePath={file.path}
                    targetLine={file.targetLine}
                    active={file.path === activeFilePath}
                    onFocused={(focused: boolean) => handleEditorFocused(file.path, focused)}
                    onSymbolsUpdate={(symbols) => handleSymbolsUpdate(file.path, symbols)}
                    onHasChanges={(path: string, hasChanges: boolean) => handleHasChanges(path, hasChanges)}
                    onClose={() => onCloseTab(file.path)}
                />
            {/each}
        {/if}
    </div>
</div>

<style>
    .editor-tabs-container {
        display: flex;
        flex-direction: column;
        height: 100%;
        overflow: hidden;
    }

    .editor-tab-bar {
        display: flex;
        align-items: stretch;
        overflow-x: auto;
        overflow-y: hidden;
        flex-shrink: 0;
        background: var(--color-surface-0);
        border-bottom: 1px solid var(--color-border);
        min-height: 36px;
        /* Hide scrollbar */
        scrollbar-width: none;
        -ms-overflow-style: none;
    }
    .editor-tab-bar::-webkit-scrollbar {
        display: none;
    }

    .editor-tab {
        display: flex;
        align-items: center;
        gap: 6px;
        padding: 6px 10px;
        font-size: 0.75rem;
        color: var(--color-text-tertiary);
        cursor: pointer;
        border-right: 1px solid var(--color-border);
        white-space: nowrap;
        flex-shrink: 0;
        max-width: 200px;
        position: relative;
        user-select: none;
        transition: background 0.1s, color 0.1s;
    }
    .editor-tab:hover {
        background: var(--color-surface-1);
        color: var(--color-text-secondary);
    }
    .editor-tab.active {
        background: var(--color-surface-1);
        color: var(--color-text-primary);
        border-bottom: 2px solid var(--color-accent);
        margin-bottom: -1px;
    }
    .editor-tab.unsaved .editor-tab-name {
        font-weight: 500;
    }

    .editor-tab-icon {
        flex-shrink: 0;
        opacity: 0.5;
        display: flex;
        align-items: center;
    }
    .editor-tab.active .editor-tab-icon {
        opacity: 0.8;
    }

    .editor-tab-name {
        flex: 1;
        min-width: 0;
        overflow: hidden;
        text-overflow: ellipsis;
        font-family: var(--font-mono);
    }
    .editor-tab-name.preview {
        font-style: italic;
    }

    .btn-close-tab {
        flex-shrink: 0;
        display: flex;
        align-items: center;
        justify-content: center;
        width: 18px;
        height: 18px;
        padding: 0;
        border: none;
        background: transparent;
        color: currentColor;
        cursor: pointer;
        border-radius: 3px;
        opacity: 0.5;
        transition: opacity 0.1s, background 0.1s;
    }
    .btn-close-tab:hover {
        opacity: 1;
        background: var(--color-surface-3);
    }
    .editor-tab.unsaved .btn-close-tab {
        color: var(--color-status-yellow, #f0c000);
    }

    .editor-tab-actions {
        display: flex;
        align-items: center;
        gap: 2px;
        padding: 0 4px;
        flex-shrink: 0;
        margin-left: auto;
    }

    .editor-icon-btn {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 28px;
        height: 28px;
        border: none;
        background: transparent;
        color: var(--color-text-tertiary);
        cursor: pointer;
        border-radius: 4px;
        flex-shrink: 0;
    }
    .editor-icon-btn:hover:not(:disabled) {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }

    .editor-save-btn {
        display: flex;
        align-items: center;
        padding: 3px 10px;
        border: none;
        border-radius: var(--radius-md);
        background: var(--color-accent-dark);
        color: white;
        cursor: pointer;
        font-size: 0.75rem;
        font-family: inherit;
        font-weight: 500;
        flex-shrink: 0;
        transition: opacity 0.15s;
    }
    .editor-save-btn:hover:not(:disabled) {
        opacity: 0.85;
    }
    .editor-save-btn:disabled {
        filter: grayscale(1);
        opacity: 0.35;
        cursor: not-allowed;
    }

    .editor-panels {
        flex: 1;
        position: relative;
        overflow: hidden;
        min-height: 0;
    }

    /* FileEditor inside panels: only active one visible */
    .editor-panels :global(> .file-editor) {
        position: absolute;
        inset: 0;
        display: none;
    }
    .editor-panels :global(> .file-editor.active) {
        display: flex;
    }

    .file-editor-loading {
        display: flex;
        align-items: center;
        justify-content: center;
        padding: 48px 24px;
        color: var(--color-text-tertiary);
        font-size: 0.8125rem;
        flex: 1;
    }
</style>
