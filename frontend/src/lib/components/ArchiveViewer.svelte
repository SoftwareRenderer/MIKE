<script lang="ts">
    import { onDestroy } from 'svelte';
    import { t } from '$lib/i18n';
    import { readFsArchive, readFsArchiveEntry } from '$lib/api';
    import type { FsArchiveEntry, FsArchiveListing } from '$lib/api';
    import { formatBytes } from '$lib/utils/format';
    import { formatDate, formatDateTime } from '$lib/utils/format-time';
    import { getLanguageSupport } from '$lib/utils/editor-language';
    import { EditorState } from '@codemirror/state';
    import { EditorView, lineNumbers } from '@codemirror/view';
    import { darkSyntax, darkTheme, isLightTheme, lightSyntax, lightTheme } from '$lib/utils/codemirror-theme';
    import Spinner from './Spinner.svelte';

    /**
     * An archive read as a list of its members instead of as bytes, in the order the archive holds them;
     * opening one reads just that member into a read-only view. Nothing is unpacked — the server walks the
     * archive and answers.
     */
    let { projectId, taskId = '', path, ref = '' }: {
        projectId: string;
        /** Task whose worktree holds the archive; omit for the project's own checkout. */
        taskId?: string;
        path: string;
        /** Read what a commit holds instead of the file on disk. */
        ref?: string;
    } = $props();

    let listing = $state<FsArchiveListing | null>(null);
    let loading = $state(true);
    let error = $state('');
    /** The member being read; empty while the listing itself is on screen. */
    let openName = $state('');
    let entryText = $state('');
    let entryLoaded = $state(false);
    let entryLoading = $state(false);
    let entryError = $state('');

    let entryEl = $state<HTMLDivElement | null>(null);
    let entryView: EditorView | null = null;

    const files = $derived(listing?.entries ?? []);
    const totalBytes = $derived(files.reduce((sum, e) => sum + e.size, 0));

    // Clicking through tabs can put two requests in flight; the older one must not arrive last and win.
    let request = 0;

    $effect(() => {
        const file = path, at = ref, project = projectId, task = taskId;
        const token = ++request;
        loading = true;
        error = '';
        listing = null;
        closeEntry();
        void (async () => {
            try {
                const answer = await readFsArchive({ project, task: task || undefined, path: file, ref: at || undefined });
                if (token !== request) return;
                listing = answer;
            } catch (e) {
                if (token !== request) return;
                error = e instanceof Error && e.message ? e.message : t('explorer.archive.failed');
            } finally {
                if (token === request) loading = false;
            }
        })();
    });

    onDestroy(() => destroyEntry());

    function destroyEntry() {
        entryView?.destroy();
        entryView = null;
    }

    function closeEntry() {
        destroyEntry();
        openName = '';
        entryText = '';
        entryLoaded = false;
        entryError = '';
        entryLoading = false;
    }

    async function openEntry(entry: FsArchiveEntry) {
        if (entry.isDir) return;
        const token = ++request;
        openName = entry.name;
        entryText = '';
        entryLoaded = false;
        entryError = '';
        entryLoading = true;
        destroyEntry();
        try {
            const content = await readFsArchiveEntry({
                project: projectId, task: taskId || undefined, path, ref: ref || undefined, entry: entry.name,
            });
            if (token !== request) return;
            entryText = content;
            entryLoaded = true;
        } catch (e) {
            if (token !== request) return;
            entryError = e instanceof Error && e.message ? e.message : t('explorer.archive.entryFailed');
        } finally {
            if (token === request) entryLoading = false;
        }
    }

    // Read-only: no history and no keymap, the file's own grammar, the shared editor theme.
    $effect(() => {
        const text = entryText;
        const name = openName;
        if (!entryEl || !name || !entryLoaded) return;
        destroyEntry();
        const light = isLightTheme();
        const language = getLanguageSupport(name);
        const state = EditorState.create({
            doc: text,
            extensions: [
                lineNumbers(),
                EditorState.readOnly.of(true),
                EditorView.editable.of(false),
                EditorView.lineWrapping,
                light ? lightTheme : darkTheme,
                light ? lightSyntax : darkSyntax,
                ...(language ? [language] : []),
            ],
        });
        entryView = new EditorView({ state, parent: entryEl });
    });
</script>

<div class="archive-viewer">
    {#if openName}
        <div class="archive-entry-bar">
            <button type="button" class="btn btn-ghost btn-small archive-back" onclick={closeEntry} title={t('explorer.archive.back')}>
                <svg width="12" height="12" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                    <path d="M10 3L5 8l5 5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                </svg>
                {t('explorer.archive.back')}
            </button>
            <span class="archive-entry-name" title={openName}>{openName}</span>
            {#if entryLoading}
                <Spinner size={12} decorative />
            {/if}
        </div>
        {#if entryError}
            <div class="archive-note archive-note-error">{entryError}</div>
        {:else if entryLoading}
            <div class="archive-placeholder"><Spinner decorative /><span>{t('explorer.archive.loadingEntry')}</span></div>
        {:else}
            <div class="archive-entry" bind:this={entryEl}></div>
        {/if}
    {:else if loading}
        <div class="archive-placeholder"><Spinner decorative /><span>{t('explorer.shared.loading')}</span></div>
    {:else if error}
        <div class="archive-note archive-note-error">{error}</div>
    {:else if files.length === 0}
        <div class="archive-placeholder">{t('explorer.archive.empty')}</div>
    {:else}
        <div class="archive-summary">
            <span>{listing?.format}</span>
            <span>{t('explorer.archive.entries', { count: files.length })}</span>
            <span>{formatBytes(totalBytes)}</span>
        </div>
        {#if listing?.truncated}
            <div class="archive-note">{t('explorer.archive.truncated')}</div>
        {/if}
        <div class="archive-scroll">
            <table class="archive-table">
                <caption class="archive-sr-only">{t('explorer.archive.listingAria')}</caption>
                <thead>
                    <tr>
                        <th scope="col">{t('explorer.archive.name')}</th>
                        <th scope="col" class="archive-col-size">{t('explorer.archive.size')}</th>
                        <th scope="col" class="archive-col-date">{t('explorer.archive.modified')}</th>
                    </tr>
                </thead>
                <tbody>
                    <!-- An archive may name two members the same thing, so a name is not an identity here;
                         the listing is fetched whole and never reordered. -->
                    {#each files as entry, i (i)}
                        <tr class:archive-row-dir={entry.isDir}>
                            <td class="archive-cell-name">
                                {#if entry.isDir}
                                    <span class="archive-name">{entry.name}/</span>
                                {:else}
                                    <button
                                        type="button"
                                        class="archive-name-btn"
                                        title={t('explorer.archive.openEntry', { name: entry.name })}
                                        onclick={() => openEntry(entry)}
                                    >{entry.name}</button>
                                {/if}
                            </td>
                            <td class="archive-cell-size">{entry.isDir ? t('explorer.archive.directory') : formatBytes(entry.size)}</td>
                            <td class="archive-cell-date" title={entry.mtime ? formatDateTime(entry.mtime) : ''}>{entry.mtime ? formatDate(entry.mtime) : ''}</td>
                        </tr>
                    {/each}
                </tbody>
            </table>
        </div>
    {/if}
</div>

<style>
    /* Fills whatever holds it: an explorer tab's pane, or a modal body. */
    .archive-viewer {
        display: flex;
        flex-direction: column;
        flex: 1;
        min-height: 0;
        height: 100%;
        background: var(--color-surface-0);
        color: var(--color-text-primary);
        font-size: var(--text-xs);
        container-type: inline-size;
        container-name: archive-view;
        --archive-size-col: 90px;
        --archive-date-col: 108px;
    }

    .archive-placeholder {
        display: flex;
        align-items: center;
        justify-content: center;
        gap: 8px;
        flex: 1;
        color: var(--color-text-tertiary);
    }
    .archive-note {
        padding: 6px 12px;
        font-size: var(--text-2xs);
        color: var(--color-text-tertiary);
        border-bottom: 1px solid var(--color-border);
    }
    .archive-note-error {
        color: var(--color-status-red);
    }
    .archive-summary {
        display: flex;
        gap: 10px;
        padding: 6px 12px;
        color: var(--color-text-tertiary);
        border-bottom: 1px solid var(--color-border);
        flex-shrink: 0;
    }
    .archive-sr-only {
        position: absolute;
        width: 1px;
        height: 1px;
        overflow: hidden;
        clip-path: inset(50%);
    }

    .archive-scroll {
        flex: 1;
        min-height: 0;
        overflow: auto;
    }
    .archive-table {
        width: 100%;
        border-collapse: collapse;
        table-layout: fixed;
    }
    .archive-table th {
        position: sticky;
        top: 0;
        background: var(--color-surface-1);
        text-align: left;
        font-weight: 600;
        font-size: var(--text-2xs);
        color: var(--color-text-tertiary);
        padding: 5px 12px;
        z-index: var(--z-sticky);
    }
    .archive-table td {
        padding: 3px 12px;
        border-top: 1px solid var(--color-border);
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }
    .archive-col-size, .archive-cell-size {
        width: var(--archive-size-col);
        text-align: right;
    }
    .archive-col-date, .archive-cell-date {
        width: var(--archive-date-col);
        color: var(--color-text-tertiary);
    }
    .archive-cell-size, .archive-cell-date {
        font-family: var(--font-mono);
    }
    .archive-cell-name {
        font-family: var(--font-mono);
    }
    .archive-name {
        color: var(--color-text-tertiary);
    }
    .archive-name-btn {
        display: block;
        width: 100%;
        text-align: left;
        padding: 0;
        border: none;
        background: none;
        color: var(--color-text-primary);
        font: inherit;
        font-family: var(--font-mono);
        cursor: pointer;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }
    .archive-name-btn:hover {
        color: var(--color-accent);
        text-decoration: underline;
    }
    .archive-row-dir .archive-cell-name {
        opacity: 0.8;
    }

    .archive-entry-bar {
        display: flex;
        align-items: center;
        gap: 8px;
        padding: 4px 8px;
        border-bottom: 1px solid var(--color-border);
        flex-shrink: 0;
    }
    .archive-entry-name {
        font-family: var(--font-mono);
        color: var(--color-text-secondary);
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }
    .archive-back {
        flex-shrink: 0;
    }
    .archive-entry {
        flex: 1;
        min-height: 0;
        overflow: auto;
    }
    .archive-entry :global(.cm-editor) {
        height: 100%;
    }

    @container archive-view (max-width: 560px) {
        .archive-table {
            min-width: 560px;
            table-layout: auto;
        }

        .archive-col-date, .archive-cell-date {
            position: sticky;
            right: 0;
            min-width: var(--archive-date-col);
            max-width: var(--archive-date-col);
        }
        .archive-col-size, .archive-cell-size {
            position: sticky;
            right: var(--archive-date-col);
            min-width: var(--archive-size-col);
            max-width: var(--archive-size-col);
        }
        .archive-cell-size, .archive-cell-date {
            background: var(--color-surface-0);
        }

        .archive-entry-bar {
            align-items: flex-start;
        }
        .archive-entry-name {
            white-space: normal;
            overflow-wrap: anywhere;
        }
    }
</style>
