<!--
    The answer to a run that stopped on a submodule the person's checkout never fetched. The listing comes
    from the server rather than from the failure's own text, so what this card names is what the next start
    will re-read.

    The URL is text, never a link. It is a repository-chosen address, and a person copying it into a terminal
    is the decision the button stands in for.
-->
<script lang="ts">
    import { t } from '$lib/i18n';
    import { getTaskSubmodules, initProjectSubmodules, type SubmoduleState } from '$lib/api/git';
    import { isAborted } from '$lib/api/helpers';
    import { dialogStore } from '$lib/stores';
    import Spinner from '$lib/components/Spinner.svelte';

    interface Props {
        projectId: string;
        taskId: string;
        /** The sentence the run ended with. It is the fallback when the listing has nothing to say, not a
         * second copy of what the rows already name. */
        message: string;
        onDismiss?: () => void;
    }

    let { projectId, taskId, message, onDismiss }: Props = $props();

    let rows = $state<SubmoduleState[]>([]);
    let loading = $state(true);
    let failed = $state('');
    let initializing = $state(false);
    /** What git said about the last click, one line per outcome. */
    let reported = $state<string[]>([]);
    let initialized = $state(false);

    const missing = $derived(rows.filter(r => !r.populated));

    async function read() {
        loading = true;
        failed = '';
        try {
            rows = await getTaskSubmodules(projectId, taskId);
        } catch (e: any) {
            if (!isAborted(e)) failed = e?.message || t('chat.submodules.readFailed');
        } finally {
            loading = false;
        }
    }

    $effect(() => {
        if (projectId && taskId) void read();
    });

    async function initialize() {
        initializing = true;
        reported = [];
        try {
            const result = await initProjectSubmodules(projectId);
            reported = result.reported;
            rows = await getTaskSubmodules(projectId, taskId);
            initialized = rows.every(r => r.populated);
            if (initialized) {
                dialogStore.showToast(t('chat.submodules.initialized'), 'success');
            }
        } catch (e: any) {
            dialogStore.showToast(e?.message || t('chat.submodules.initFailed'), 'error');
        } finally {
            initializing = false;
        }
    }
</script>

<div class="submodule-notice">
    <div class="notice-header">
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <path d="M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z"/>
            <polyline points="3.27 6.96 12 12.01 20.73 6.96"/>
            <line x1="12" y1="22.08" x2="12" y2="12"/>
        </svg>
        <span class="notice-title">{initialized ? t('chat.submodules.titleDone') : t('chat.submodules.title')}</span>
        {#if onDismiss}
            <button type="button" class="btn-dismiss" onclick={onDismiss} title={t('chat.entries.dismissError')} aria-label={t('chat.entries.dismissError')}>
                <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                    <line x1="18" y1="6" x2="6" y2="18"/>
                    <line x1="6" y1="6" x2="18" y2="18"/>
                </svg>
            </button>
        {/if}
    </div>

    {#if loading}
        <div class="notice-line"><Spinner size={12} decorative /> {t('chat.submodules.reading')}</div>
    {:else if failed}
        <div class="notice-line notice-error">{failed}</div>
    {:else if rows.length === 0}
        <div class="notice-line">{t('chat.submodules.none')}</div>
    {:else}
        <ul class="submodule-list">
            {#each rows as row (row.path)}
                <li class="submodule-row">
                    <span class="submodule-path">{row.path}</span>
                    {#if row.url}<span class="submodule-url">{row.url}</span>{/if}
                    {#if row.note}<span class="submodule-note">{row.note}</span>{/if}
                    {#if row.populated}<span class="submodule-state">{t('chat.submodules.present')}</span>{/if}
                </li>
            {/each}
        </ul>
    {/if}

    {#if reported.length > 0}
        <ul class="reported-list">
            {#each reported as line, i (i)}
                <li class="notice-line">{line}</li>
            {/each}
        </ul>
    {/if}

    <div class="notice-actions">
        {#if !initialized && (missing.length > 0 || rows.length === 0)}
            <button type="button" class="btn btn-secondary" onclick={initialize} disabled={initializing}>
                {#if initializing}
                    <Spinner size={12} decorative />
                    <span>{t('chat.submodules.initializing')}</span>
                {:else}
                    <span>{t('chat.submodules.initialize')}</span>
                {/if}
            </button>
        {/if}
    </div>

    {#if !loading && (failed || rows.length === 0)}
        <p class="notice-detail">{message}</p>
    {/if}
</div>

<style>
    .submodule-notice {
        display: flex;
        flex-direction: column;
        gap: 8px;
        padding: 10px 14px;
        border: 1px solid color-mix(in srgb, var(--color-status-orange) 30%, transparent);
        border-left: 3px solid var(--color-status-orange);
        border-radius: var(--radius-md);
        background: color-mix(in srgb, var(--color-status-orange) 6%, transparent);
        font-size: 13px;
    }

    .notice-header {
        display: flex;
        align-items: center;
        gap: 6px;
        color: var(--color-status-orange);
    }

    .notice-header svg { flex-shrink: 0; }

    .notice-title {
        font-weight: 600;
    }

    .btn-dismiss {
        margin-left: auto;
        display: inline-flex;
        align-items: center;
        justify-content: center;
        width: 18px;
        height: 18px;
        border: none;
        background: none;
        color: var(--color-text-muted);
        cursor: pointer;
        padding: 0;
    }

    .submodule-list,
    .reported-list {
        display: flex;
        flex-direction: column;
        gap: 4px;
        margin: 0;
        padding: 0;
        list-style: none;
    }

    .submodule-row {
        display: flex;
        flex-wrap: wrap;
        align-items: baseline;
        gap: 6px;
    }

    .submodule-path,
    .submodule-url {
        font-family: var(--font-mono);
    }

    .submodule-url,
    .submodule-note,
    .notice-line,
    .notice-detail {
        color: var(--color-text-secondary);
    }

    .submodule-url {
        overflow-wrap: anywhere;
    }

    .submodule-state {
        color: var(--color-status-green);
    }

    .notice-error {
        color: var(--color-status-red);
    }

    .notice-actions {
        display: flex;
        gap: 8px;
    }

    .notice-detail {
        margin: 0;
        overflow-wrap: anywhere;
    }
</style>
