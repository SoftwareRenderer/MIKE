<script lang="ts">
    import { onMount, tick } from 'svelte';
    import BaseModal from './BaseModal.svelte';
    import { listFsDirectory, createFsDirectory, initGitRepository, getHomeDirectory, type FsEntry } from '$lib/api';
    import { errorText } from '$lib/utils/error-text';
    import Icon from '$lib/components/Icon.svelte';
    import { uiInjector, INJECTION_POINTS } from '$lib/services/ui-injector.svelte';
    // UI extension injection point — re-render when extensions change
    $effect(() => {
        const _v = uiInjector.getPointVersion(INJECTION_POINTS.FILE_BROWSER);
        uiInjector.refreshSlot(INJECTION_POINTS.FILE_BROWSER, { component: 'FileBrowser' });
    });
    import homeIcon from '$lib/assets/icons/home.svg?raw';
    import { t } from '$lib/i18n';
    import folderIcon from '$lib/assets/icons/folder.svg?raw';
    import fileIcon from '$lib/assets/icons/file.svg?raw';

    let {
        onClose,
        onSelect,
        onClone,
    }: {
        onClose: () => void;
        onSelect: (path: string, sandboxEnabled: boolean) => void;
        onClone: (gitUrl: string, targetDir: string, sandboxEnabled: boolean) => Promise<void>;
    } = $props();

    let activeTab = $state<'browse' | 'clone'>('browse');

    let sandboxEnabled = $state(true);

    let currentPath = $state('');
    let entries = $state<FsEntry[]>([]);
    let isGitRepo = $state(false);
    let loading = $state(false);
    let error = $state('');
    let initGitLoading = $state(false);
    let showNewFolder = $state(false);
    let newFolderName = $state('');
    let newFolderInput: HTMLInputElement | null = $state(null);
    let cloneUrlInput: HTMLInputElement | null = $state(null);
    let mkdirLoading = $state(false);

    let cloneUrl = $state('');
    let cloneLoading = $state(false);
    let cloneError = $state('');

    /**
     * Request sequence for `loadDir`: loads overlap and responses have no ordering guarantee,
     * so without this a stale reply lands last and the list (and `isGitRepo`, hence the
     * indicator and the Select button) describes a directory other than `currentPath`.
     */
    let loadSeq = 0;

    async function loadDir(path: string) {
        const seq = ++loadSeq;
        loading = true;
        error = '';
        currentPath = path;
        try {
            const listing = await listFsDirectory({ path });
            if (seq !== loadSeq) return; // a newer load won — keep its result
            entries = listing.entries;
            isGitRepo = listing.isGitRepo;
        } catch (e) {
            if (seq !== loadSeq) return;
            error = errorText(e, t('explorer.shared.errReadDir'));
            entries = [];
        } finally {
            if (seq === loadSeq) loading = false;
        }
    }

    function handleClick(entry: FsEntry) {
        if (entry.isGitRepo) {
            onSelect(entry.path, sandboxEnabled);
        } else if (entry.isDir) {
            loadDir(entry.path);
        }
    }

    function handleSelect() {
        if (isGitRepo) {
            onSelect(currentPath, sandboxEnabled);
        }
    }

    function handleInputChange() {
        const p = currentPath.trim();
        if (p) {
            loadDir(p);
        }
    }

    async function handleInitGit() {
        initGitLoading = true;
        error = '';
        try {
            await initGitRepository({ path: currentPath });
            // Refresh directory to confirm git init succeeded, then auto-select
            await loadDir(currentPath);
            onSelect(currentPath, sandboxEnabled);
        } catch (e) {
            error = errorText(e, t('explorer.shared.errInitGit'));
        } finally {
            initGitLoading = false;
        }
    }

    async function toggleNewFolder() {
        if (!showNewFolder) {
            newFolderName = '';
        }
        showNewFolder = !showNewFolder;
        if (showNewFolder) {
            await tick(); // wait for the row to render
            newFolderInput?.focus();
        }
    }

    async function handleCreateFolder() {
        const name = newFolderName.trim();
        if (!name) return;
        mkdirLoading = true;
        error = '';
        try {
            const newPath = currentPath.endsWith('/') ? currentPath + name : currentPath + '/' + name;
            await createFsDirectory({ path: newPath });
            showNewFolder = false;
            newFolderName = '';
            await loadDir(newPath);
        } catch (e) {
            error = errorText(e, t('explorer.shared.errCreateFolder'));
        } finally {
            mkdirLoading = false;
        }
    }

    async function handleClone() {
        const url = cloneUrl.trim();
        if (!url) {
            cloneError = t('explorer.fileBrowser.cloneNeedUrl');
            return;
        }
        cloneError = '';
        cloneLoading = true;
        try {
            await onClone(url, currentPath, sandboxEnabled);
        } catch (e) {
            cloneError = errorText(e, t('api.cloneFailed'));
        } finally {
            cloneLoading = false;
        }
    }

    async function switchTab(tab: 'browse' | 'clone') {
        cloneError = '';
        activeTab = tab;
        if (tab === 'clone') {
            await tick();
            cloneUrlInput?.focus();
        }
    }

    onMount(() => { goHome(); });

    /** The home button and the mount share this, so both land on the same directory. */
    async function goHome() {
        try {
            await loadDir(await getHomeDirectory() || '/home');
        } catch {
            await loadDir('/home');
        }
    }

    function formatPath(p: string): string {
        return p || '/';
    }

    function handleClose() {
        onClose();
    }
</script>

<BaseModal title={t('explorer.fileBrowser.title')} width="560px" modalClass="path-browser" onClose={handleClose}>
        {#snippet toolbar()}
            <div class="tabs">
                <button
                    type="button"
                    class={`tab${activeTab === 'browse' ? ' active' : ''}`}
                    onclick={() => switchTab('browse')}
                    title={t('explorer.fileBrowser.browseTitle')}
                >
                    {t('explorer.fileBrowser.browse')}
                </button>
                <button
                    type="button"
                    class={`tab${activeTab === 'clone' ? ' active' : ''}`}
                    onclick={() => switchTab('clone')}
                    title={t('explorer.fileBrowser.cloneTitle')}
                >
                    {t('explorer.fileBrowser.cloneTab')}
                </button>
            </div>
            {#if activeTab === 'browse'}
                <div class="path-bar">
                <div class="path-bar-row">
                    <button class="home-btn" onclick={goHome} aria-label={t('explorer.shared.goHome')} title={t('explorer.shared.goHome')}>
                        <Icon src={homeIcon} color="#8B949E" width={18} height={18} />
                    </button>
                    <input
                        type="text"
                        class="path-input"
                        bind:value={currentPath}
                        onkeydown={(e) => { if (e.key === 'Enter') handleInputChange(); }}
                        placeholder={t('explorer.shared.pathPlaceholder')}
                        aria-label={t('explorer.shared.filePathAria')}
                    />
                    <button class="icon-btn-sm" onclick={toggleNewFolder} aria-label={t('explorer.shared.newFolder')} title={t('explorer.shared.newFolder')} disabled={loading}>
                        <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                            <path d="M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z"/>
                            <line x1="12" y1="10" x2="12" y2="16"/>
                            <line x1="9" y1="13" x2="15" y2="13"/>
                        </svg>
                    </button>
                </div>
                {#if showNewFolder}
                    <div class="new-folder-row">
                        <input
                            type="text"
                            class="folder-name-input"
                            bind:this={newFolderInput}
                            bind:value={newFolderName}
                            placeholder={t('explorer.fileBrowser.folderNamePlaceholder')}
                            onkeydown={(e) => { if (e.key === 'Enter') handleCreateFolder(); if (e.key === 'Escape') showNewFolder = false; }}
                            aria-label={t('explorer.shared.newFolderNameAria')}
                        />
                        <button class="btn-mini" onclick={handleCreateFolder} disabled={mkdirLoading || !newFolderName.trim()} title={t('explorer.fileBrowser.create')}>{mkdirLoading ? '...' : t('explorer.fileBrowser.create')}</button>
                        <button class="btn-mini btn-mini-cancel" onclick={() => { showNewFolder = false; newFolderName = ''; }} title={t('explorer.shared.cancel')}>✕</button>
                    </div>
                {/if}
                </div>
            {/if}
        {/snippet}

        <div class="body-wrapper">
            {#if activeTab === 'browse'}
                {#if error}
                    <div class="error">{error}</div>
                {/if}

                {#if loading}
                    <div class="loading">{t('explorer.shared.loading')}</div>
                {:else}
                    <!-- Git indicator -->
                    {#if isGitRepo}
                        <div class="git-indicator">{t('explorer.fileBrowser.isGitRepo')}</div>
                    {/if}

                    <!-- File list -->
                    <div class="file-list" role="list" aria-label={t('explorer.shared.contentsAria')}>
                        {#each entries as entry}
                            <button
                                type="button"
                                class={`file-entry${entry.isDir ? ' dir' : ''}${entry.isGitRepo ? ' git-repo' : ''}`}
                                onclick={() => handleClick(entry)}
                                aria-label={entry.isDir
                                    ? (entry.isGitRepo
                                        ? t('explorer.fileBrowser.entryFolderGit', { name: entry.name })
                                        : t('explorer.shared.entryFolder', { name: entry.name }))
                                    : (entry.isGitRepo
                                        ? t('explorer.fileBrowser.entryFileGit', { name: entry.name })
                                        : t('explorer.shared.entryFile', { name: entry.name }))}
                                title={entry.isDir
                                    ? (entry.isGitRepo
                                        ? t('explorer.fileBrowser.openFolderGit', { name: entry.name })
                                        : t('explorer.shared.openFolder', { name: entry.name }))
                                    : (entry.isGitRepo
                                        ? t('explorer.fileBrowser.entryFileGitTitle', { name: entry.name })
                                        : t('explorer.shared.entryFile', { name: entry.name }))}
                            >
                                <span class="icon" aria-hidden="true">
                                    {#if entry.isDir}
                                        <Icon src={folderIcon} color="#6E7681" width={16} height={16} />
                                    {:else}
                                        <Icon src={fileIcon} color="#6E7681" width={16} height={16} />
                                    {/if}
                                </span>
                                <span class="name">{entry.name}</span>
                                {#if entry.isGitRepo}
                                    <span class="git-badge">git</span>
                                {/if}
                            </button>
                        {/each}
                    </div>
                {/if}
            {:else}
                <div class="clone-form">
                    <label class="clone-label" for="clone-url">{t('explorer.fileBrowser.cloneUrl')}</label>
                    <input
                        id="clone-url"
                        type="text"
                        class="clone-url-input"
                        bind:this={cloneUrlInput}
                        bind:value={cloneUrl}
                        placeholder="https://github.com/org/repo.git"
                        onkeydown={(e) => { if (e.key === 'Enter' && !cloneLoading) handleClone(); }}
                        aria-label={t('explorer.fileBrowser.cloneUrl')}
                    />
                    <div class="clone-help">{t('explorer.fileBrowser.cloneHelpBefore')}<code>git@github.com:org/repo.git</code>{t('explorer.fileBrowser.cloneHelpAfter')}</div>

                    <div class="clone-target">
                        <span class="clone-target-label">{t('explorer.fileBrowser.cloneInto')}</span>
                        <span class="clone-target-path">{formatPath(currentPath)}</span>
                    </div>

                    {#if cloneError}
                        <div class="error">{cloneError}</div>
                    {/if}
                </div>
            {/if}
        </div>

        {#snippet footer()}
            <div class="footer-wrapper">
            <span class="current-path">{formatPath(currentPath)}</span>
            <div class="footer-actions">
                <label class="sandbox-toggle">
                    <input type="checkbox" checked={sandboxEnabled} onchange={(e) => sandboxEnabled = e.currentTarget.checked} />
                    <span>{t('explorer.fileBrowser.useSandbox')}</span>
                </label>
                <button type="button" class="btn" onclick={handleClose} title={t('explorer.shared.cancel')}>{t('explorer.shared.cancel')}</button>
                {#if activeTab === 'browse'}
                    {#if isGitRepo}
                        <button type="button" class="btn btn-primary" onclick={handleSelect} title={t('explorer.fileBrowser.selectTitle')}>
                            {t('explorer.fileBrowser.select')}
                        </button>
                    {:else}
                        <button type="button" class="btn btn-primary" onclick={handleInitGit} disabled={initGitLoading} title={t('explorer.fileBrowser.initGitTitle')}>
                            {initGitLoading ? t('explorer.fileBrowser.initializing') : t('explorer.fileBrowser.initGit')}
                        </button>
                    {/if}
                {:else}
                    <button type="button" class="btn btn-primary" onclick={handleClone} disabled={cloneLoading || !cloneUrl.trim()} title={t('explorer.fileBrowser.cloneIntoDirTitle')}>
                        {cloneLoading ? t('explorer.fileBrowser.cloning') : t('explorer.fileBrowser.clone')}
                    </button>
                {/if}
                </div>
            </div>
        {/snippet}
</BaseModal>

<style>
    @import '$lib/styles/path-browser.css';

    :global(.modal) {
        display: flex;
        flex-direction: column;
        max-height: 80vh;
    }
    :global(.modal-body) {
        flex: 1;
        overflow-y: auto;
        padding: 0 16px 16px;
        display: flex;
        flex-direction: column;
        gap: 8px;
    }
    :global(.modal-footer) {
        gap: 12px;
    }
    .body-wrapper,
    .footer-wrapper {
        display: contents;
    }
    .tabs {
        display: flex;
        gap: 4px;
        margin-bottom: 8px;
        background: var(--color-surface-2);
        border-radius: var(--radius-md);
        padding: 4px;
    }
    .tab {
        flex: 1;
        padding: 8px 12px;
        background: transparent;
        border: none;
        border-radius: var(--radius-sm);
        color: var(--color-text-secondary);
        font-size: var(--text-base);
        font-weight: 500;
        cursor: pointer;
        white-space: nowrap;
        text-align: center;
    }
    .tab:hover {
        color: var(--color-text-primary);
        background: var(--color-surface-3);
    }
    .tab.active {
        background: var(--color-accent-dark);
        color: white;
    }
    .tab.active:hover {
        background: var(--color-accent-hover);
        color: white;
    }
    .path-bar {
        display: flex;
        flex-direction: column;
        gap: 8px;
    }
    .home-btn {
        display: flex;
        align-items: center;
        justify-content: center;
        padding: 6px;
        background: transparent;
        border-radius: var(--radius-sm);
    }
    .path-input:focus-visible {
        border-color: var(--color-accent);
        outline: none;
    }
    .error {
        color: var(--color-status-red);
        font-size: var(--text-base);
        padding: 8px;
        background: var(--color-status-red)11;
        border-radius: var(--radius-sm);
    }
    .git-indicator {
        color: var(--color-status-green);
        font-size: var(--text-base);
        padding: 8px;
        background: var(--color-status-green)11;
        border-radius: var(--radius-sm);
        text-align: center;
    }
    .new-folder-row {
        display: flex;
        gap: 8px;
        align-items: stretch;
    }
    .folder-name-input {
        flex: 1;
        padding: 8px 12px;
        background: var(--color-surface-2);
        border: 1px solid var(--color-accent);
        border-radius: var(--radius-md);
        color: var(--color-text-primary);
        font-size: var(--text-base);
        font-family: var(--font-mono);
        outline: none;
    }
    .clone-form {
        display: flex;
        flex-direction: column;
        gap: 12px;
        padding: 8px 0;
    }
    .clone-label {
        font-size: var(--text-base);
        font-weight: 600;
        color: var(--color-text-primary);
    }
    .clone-url-input {
        width: 100%;
        padding: 10px 12px;
        background: var(--color-surface-2);
        border: 1px solid var(--color-surface-4);
        border-radius: var(--radius-md);
        color: var(--color-text-primary);
        font-size: var(--text-base);
        font-family: var(--font-mono);
        outline: none;
    }
    .clone-url-input:focus-visible {
        border-color: var(--color-accent);
    }
    .clone-help {
        font-size: var(--text-sm);
        color: var(--color-text-tertiary);
    }
    .clone-help code {
        font-family: var(--font-mono);
        background: var(--color-surface-2);
        padding: 1px 4px;
        border-radius: var(--radius-sm);
    }
    .clone-target {
        display: flex;
        align-items: center;
        gap: 8px;
        padding: 10px 12px;
        background: var(--color-surface-2);
        border-radius: var(--radius-md);
    }
    .clone-target-label {
        font-size: var(--text-sm);
        color: var(--color-text-secondary);
        white-space: nowrap;
    }
    .clone-target-path {
        flex: 1;
        font-size: var(--text-sm);
        font-family: var(--font-mono);
        color: var(--color-text-primary);
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }
    .btn-mini {
        padding: 8px 12px;
        background: var(--color-accent-dark);
        color: white;
        border-radius: var(--radius-md);
        font-size: var(--text-base);
        font-weight: 500;
        white-space: nowrap;
    }
    .btn-mini:hover:not(:disabled) {
        background: var(--color-accent-hover);
    }
    .btn-mini-cancel {
        background: var(--color-surface-3);
        color: var(--color-text-secondary);
    }
    .btn-mini-cancel:hover {
        background: var(--color-surface-4);
    }
    .icon-btn-sm {
        padding: 6px;
        background: transparent;
        border-radius: var(--radius-sm);
        color: var(--color-text-secondary);
        display: flex;
        align-items: center;
        justify-content: center;
        flex-shrink: 0;
    }
    .icon-btn-sm:hover:not(:disabled) {
        background: var(--color-surface-4);
        color: var(--color-text-primary);
    }
    .file-entry.git-repo:hover {
        background: var(--color-status-green)11;
    }
    .git-badge {
        font-size: var(--text-xs);
        padding: 1px 6px;
        background: var(--color-status-green)33;
        color: var(--color-status-green);
        border-radius: var(--radius-lg);
        font-weight: 600;
    }
    .sandbox-toggle {
        display: flex;
        align-items: center;
        gap: 6px;
        font-size: var(--text-sm);
        color: var(--color-text-secondary);
        cursor: pointer;
        user-select: none;
        white-space: nowrap;
    }
    .sandbox-toggle input[type="checkbox"] {
        cursor: pointer;
        accent-color: var(--color-primary);
    }
</style>
