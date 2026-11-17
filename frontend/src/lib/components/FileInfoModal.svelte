<script lang="ts">
    import BaseModal from './BaseModal.svelte';
    import Spinner from './Spinner.svelte';
    import { statFsPaths, type FsStatRow } from '$lib/api';
    import { t, type MessageKey } from '$lib/i18n';
    import { formatBytes } from '$lib/utils/format';
    import { formatDateTime } from '$lib/utils/format-time';

    /**
     * One table, one row per asked path in the order it was asked, under a line that counts the whole
     * selection. A path gone since the listing was read is a row rather than a disappearance.
     */
    let { project, task = '', paths, ignored = {}, onClose }: {
        project: string;
        /** Task whose worktree holds the paths; empty is the project's own checkout. */
        task?: string;
        paths: string[];
        /** git's verdict for a path, which the caller holds from its listing — this route asks git nothing. */
        ignored?: Record<string, boolean>;
        onClose: () => void;
    } = $props();

    const kindKeys = {
        file: 'explorer.fileInfo.kindFile',
        dir: 'explorer.fileInfo.kindFolder',
        link: 'explorer.fileInfo.kindLink',
        other: 'explorer.fileInfo.kindOther',
        missing: 'explorer.fileInfo.kindMissing',
        unknown: 'explorer.fileInfo.kindUnknown',
    } as const satisfies Record<FsStatRow['kind'], MessageKey>;

    let rows = $state<FsStatRow[]>([]);
    let loading = $state(true);
    let error = $state('');

    /** What is known of the selection: bytes are summed only where they were measured. */
    let totals = $derived.by(() => {
        let bytes = 0;
        let folders = 0;
        let missing = 0;
        let partial = false;
        for (const row of rows) {
            bytes += row.bytes ?? 0;
            if (row.kind === 'dir') folders++;
            if (row.kind === 'missing') missing++;
            if (row.partial) partial = true;
        }
        return { bytes, folders, missing, partial };
    });

    let title = $derived(rows.length === 1 ? rows[0].name : t('explorer.fileInfo.title'));

    $effect(() => {
        const asked = paths, projectScope = project, taskScope = task;
        void (async () => {
            loading = true;
            error = '';
            try {
                rows = await statFsPaths({
                    project: projectScope, task: taskScope || undefined, paths: asked,
                });
            } catch (e) {
                error = e instanceof Error && e.message ? e.message : t('explorer.fileInfo.failed');
            } finally {
                loading = false;
            }
        })();
    });

    function size(row: FsStatRow): string {
        if (row.bytes === undefined) return '';
        const shown = formatBytes(row.bytes);
        return row.partial ? t('explorer.fileInfo.atLeast', { size: shown }) : shown;
    }
</script>

<BaseModal title={title} width="min(760px, 92vw)" {onClose}>
    {#if loading}
        <div class="fi-placeholder"><Spinner decorative /><span>{t('explorer.shared.loading')}</span></div>
    {:else if error}
        <div class="fi-note">{error}</div>
    {:else}
        <div class="fi-summary">
            <span>{t('explorer.fileInfo.entries', { count: rows.length })}</span>
            <span>{totals.partial
                ? t('explorer.fileInfo.atLeast', { size: formatBytes(totals.bytes) })
                : formatBytes(totals.bytes)}</span>
            {#if totals.folders > 0}
                <span>{t('explorer.fileInfo.folders', { count: totals.folders })}</span>
            {/if}
            {#if totals.missing > 0}
                <span class="fi-missing">{t('explorer.fileInfo.missing', { count: totals.missing })}</span>
            {/if}
        </div>
        <div class="fi-scroll">
            <table class="fi-table">
                <caption class="fi-sr-only">{t('explorer.fileInfo.listingAria')}</caption>
                <thead>
                    <tr>
                        <th scope="col">{t('explorer.fileInfo.name')}</th>
                        <th scope="col">{t('explorer.fileInfo.kind')}</th>
                        <th scope="col" class="fi-col-size">{t('explorer.fileInfo.size')}</th>
                        <th scope="col" class="fi-col-date">{t('explorer.fileInfo.modified')}</th>
                    </tr>
                </thead>
                <tbody>
                    <!-- The asked paths may repeat and a name is not an identity across two folders, so
                         the row is keyed by position — as the archive listing's are. -->
                    {#each rows as row, i (i)}
                        <tr class:fi-row-gone={row.kind === 'missing'}>
                            <td class="fi-cell-name">
                                <span class="fi-name" title={row.name}>{row.name}</span>
                                <span class="fi-path" title={row.path}>{row.path}</span>
                                {#if row.mode || row.mime}
                                    <span class="fi-meta">
                                        {#if row.mode}
                                            <span title={t('explorer.fileInfo.permissions')}>{row.mode}</span>
                                        {/if}
                                        {#if row.mode && row.mime}
                                            <span class="fi-sep" aria-hidden="true">·</span>
                                        {/if}
                                        {#if row.mime}
                                            <span title={t('explorer.fileInfo.type')}>{row.mime}</span>
                                        {/if}
                                    </span>
                                {/if}
                            </td>
                            <td>
                                {t(kindKeys[row.kind])}
                                {#if ignored[row.path]}
                                    <span class="fi-ignored">{t('explorer.fileInfo.ignored')}</span>
                                {/if}
                            </td>
                            <td class="fi-cell-size">
                                {size(row)}
                                {#if row.items !== undefined}
                                    <span class="fi-items">{t('explorer.fileInfo.entries', { count: row.items })}</span>
                                {/if}
                            </td>
                            <td class="fi-cell-date">{row.modified ? formatDateTime(row.modified) : ''}</td>
                        </tr>
                    {/each}
                </tbody>
            </table>
        </div>
    {/if}
</BaseModal>

<style>
    .fi-placeholder {
        display: flex;
        align-items: center;
        justify-content: center;
        gap: 8px;
        min-height: 120px;
        color: var(--color-text-tertiary);
    }
    .fi-note {
        padding: 12px 0;
        color: var(--color-status-red);
        font-size: var(--text-sm);
    }
    .fi-summary {
        display: flex;
        flex-wrap: wrap;
        gap: 10px;
        padding: 4px 0 8px;
        color: var(--color-text-tertiary);
        font-size: var(--text-xs);
    }
    .fi-missing {
        color: var(--color-status-amber);
    }

    .fi-scroll {
        max-height: min(56vh, 480px);
        overflow: auto;
    }
    .fi-table {
        width: 100%;
        border-collapse: collapse;
        table-layout: fixed;
        font-size: var(--text-xs);
    }
    .fi-table th {
        position: sticky;
        top: 0;
        background: var(--color-surface-1);
        text-align: left;
        font-weight: 600;
        font-size: var(--text-2xs);
        color: var(--color-text-tertiary);
        padding: 5px 8px;
        z-index: var(--z-sticky);
    }
    .fi-table td {
        padding: 5px 8px;
        border-top: 1px solid var(--color-border);
        vertical-align: top;
        color: var(--color-text-primary);
    }
    .fi-sr-only {
        position: absolute;
        width: 1px;
        height: 1px;
        overflow: hidden;
        clip-path: inset(50%);
    }
    .fi-col-size, .fi-cell-size {
        width: 132px;
        text-align: right;
    }
    .fi-col-date, .fi-cell-date {
        width: 168px;
        white-space: nowrap;
        color: var(--color-text-secondary);
    }
    .fi-cell-name {
        min-width: 0;
    }
    .fi-name {
        display: block;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }
    .fi-path, .fi-meta {
        display: block;
        font-size: var(--text-2xs);
        color: var(--color-text-tertiary);
        /* A path is one long token: it breaks mid-word rather than pushing the table wide. */
        overflow-wrap: anywhere;
    }
    .fi-sep {
        padding: 0 4px;
    }
    .fi-items, .fi-ignored {
        display: block;
        font-size: var(--text-2xs);
        color: var(--color-text-tertiary);
    }
    .fi-row-gone .fi-name, .fi-row-gone .fi-path {
        color: var(--color-text-tertiary);
        font-style: italic;
    }
</style>
