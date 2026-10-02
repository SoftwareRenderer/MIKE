<script lang="ts">
    import BaseModal from './BaseModal.svelte';
    import { Z } from '$lib/constants/z-layers';
    import Icon from '$lib/components/Icon.svelte';
    import homeIcon from '$lib/assets/icons/home.svg?raw';
    import folderIcon from '$lib/assets/icons/folder.svg?raw';
    import fileIcon from '$lib/assets/icons/file.svg?raw';
    import { t } from '$lib/i18n';
    import { listFsDirectory, resolveHomeDirectory, type FsEntry as Entry } from '$lib/api';
    import { parentDir } from '$lib/utils/path';

    let { title, initialPath = '', emptyLabel, selectTitle, fileFilter, onClose, onSelect }: {
        title: string;
        /** The directory to open on. Empty opens on the server's home, the root a host browse is jailed to. */
        initialPath?: string;
        /** Shown when a directory lists nothing selectable. */
        emptyLabel: string;
        /** Tooltip on the footer Select button. */
        selectTitle: string;
        /**
         * When provided, the picker runs in file mode: files matching this
         * predicate are listed alongside directories and are the selectable
         * entries. Without it the picker is directory-only and every loaded
         * directory itself is the selection.
         */
        fileFilter?: (name: string) => boolean;
        onClose: () => void;
        onSelect: (path: string) => void;
    } = $props();

    const fileMode = $derived(fileFilter !== undefined);

    let currentPath = $state('');
    let entries = $state<Entry[]>([]);
    /** Directory mode: the loaded directory; file mode: the picked file. */
    let selected = $state<string | null>(null);
    let loading = $state(false);
    let error = $state('');
    let parentPath = $derived(
        currentPath && currentPath !== '/' ? (parentDir(currentPath) || '/') : null
    );

    async function loadDir(path: string) {
        loading = true;
        error = '';
        currentPath = path;
        selected = fileMode ? null : path; // selecting the directory itself
        try {
            const listing = await listFsDirectory({ path, files: fileMode });
            entries = listing.entries.filter(e => e.isDir || (fileFilter && fileFilter(e.name)));
        } catch (e) {
            error = String(e);
            entries = [];
        } finally {
            loading = false;
        }
    }

    function handleClick(entry: Entry) {
        if (entry.isDir) {
            loadDir(entry.path);
        } else {
            selected = entry.path;
        }
    }

    function handleSelect() {
        if (selected) {
            onSelect(selected);
        }
    }

    function handleInputChange() {
        const p = currentPath.trim();
        if (p) {
            loadDir(p);
        }
    }

    let homePath = '';

    async function goHome() {
        homePath ||= await resolveHomeDirectory();
        await loadDir(homePath);
    }

    function formatPath(p: string): string {
        return p || '/';
    }

    // Open on the path the caller named, or on home when it named none.
    $effect(() => {
        if (initialPath) {
            loadDir(initialPath);
        } else {
            void goHome();
        }
    });
</script>

{#snippet footer()}
    <span class="current-path">{formatPath(selected ?? currentPath)}</span>
    <div class="footer-actions">
        <button type="button" class="btn" onclick={onClose} title={t('explorer.shared.cancel')}>{t('explorer.shared.cancel')}</button>
        <button type="button" class="btn btn-primary" onclick={handleSelect} disabled={!selected} title={selectTitle}>
            {t('explorer.pathPicker.select')}
        </button>
    </div>
{/snippet}

<BaseModal {title} width="560px" zIndex={Z.modalStacked} modalClass="path-browser" maxHeight="80vh" footer={footer} onClose={onClose}>
    <!-- Path bar -->
    <div class="path-bar">
        <div class="path-bar-row">
            <button type="button" class="home-btn" onclick={() => { void goHome(); }} aria-label={t('explorer.shared.goHome')} title={t('explorer.shared.goHome')}>
                <Icon src={homeIcon} color="var(--color-text-tertiary)" width={18} height={18} />
            </button>
            <input
                type="text"
                class="path-input"
                bind:value={currentPath}
                onkeydown={(e) => { if (e.key === 'Enter') handleInputChange(); }}
                placeholder={t('explorer.shared.pathPlaceholder')}
                aria-label={fileMode ? t('explorer.shared.filePathAria') : t('explorer.pathPicker.dirPathAria')}
            />
        </div>
    </div>

    {#if loading}
        <div class="loading">{t('explorer.shared.loading')}</div>
    {:else}
        {#if error}
            <div class="error">{error}</div>
            {#if parentPath}
                <button
                    type="button"
                    class="file-entry dir parent-entry"
                    onclick={() => loadDir(parentPath)}
                    aria-label={t('explorer.pathPicker.goParentAria')}
                    title={t('explorer.pathPicker.goParentAria')}
                >
                    <span class="icon" aria-hidden="true">
                        <Icon src={folderIcon} color="var(--color-text-muted)" width={16} height={16} />
                    </span>
                    <span class="name">..</span>
                </button>
            {/if}
        {:else if entries.length === 0}
            <div class="empty">{emptyLabel}</div>
        {:else}
            <div class="file-list" role="list" aria-label={t('explorer.shared.contentsAria')}>
                {#each entries as entry}
                    <button
                        type="button"
                        class="file-entry"
                        class:dir={entry.isDir}
                        class:selected={selected === entry.path && fileMode}
                        onclick={() => handleClick(entry)}
                        aria-label={entry.isDir
                            ? t('explorer.shared.entryFolder', { name: entry.name })
                            : t('explorer.shared.entryFile', { name: entry.name })}
                        title={entry.isDir
                            ? t('explorer.shared.openFolder', { name: entry.name })
                            : t('explorer.pathPicker.selectFile', { name: entry.name })}
                    >
                        <span class="icon" aria-hidden="true">
                            {#if entry.isDir}
                                <Icon src={folderIcon} color="var(--color-text-muted)" width={16} height={16} />
                            {:else}
                                <Icon src={fileIcon} color="var(--color-text-muted)" width={16} height={16} />
                            {/if}
                        </span>
                        <span class="name">{entry.name}</span>
                    </button>
                {/each}
            </div>
        {/if}
    {/if}
</BaseModal>

<style>
    @import '$lib/styles/shared.css';
    @import '$lib/styles/path-browser.css';

    :global(.path-browser .modal-body) {
        display: flex;
        flex-direction: column;
        gap: 8px;
    }

    .path-bar {
        display: flex;
        flex-direction: column;
        gap: 8px;
        position: sticky;
        top: 0;
        z-index: var(--z-sticky);
        background: var(--color-surface-1);
        padding: 12px 0 8px;
    }
    .home-btn {
        display: flex;
        align-items: center;
        justify-content: center;
        padding: 6px;
        background: transparent;
        border-radius: var(--radius-sm);
        border: none;
        cursor: pointer;
        color: var(--color-text-secondary);
    }
    .error {
        color: var(--color-status-red);
        font-size: var(--text-base);
        padding: 8px;
        background: color-mix(in srgb, var(--color-status-red) 10%, transparent);
        border-radius: var(--radius-sm);
    }
    .empty {
        text-align: center;
        color: var(--color-text-secondary);
        padding: 24px;
        font-size: var(--text-base);
    }
    .file-entry.selected {
        background: var(--color-accent-dark);
        color: var(--color-btn-text);
    }
    .file-entry.parent-entry {
        color: var(--color-text-secondary);
        font-style: italic;
    }
</style>
