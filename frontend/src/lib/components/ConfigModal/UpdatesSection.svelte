<script lang="ts">
    import { t } from '$lib/i18n';
    import ToggleSwitch from '../ToggleSwitch.svelte';
    import { updateCheck } from '$lib/services/update-check.svelte';
    import { formatRelativeTime } from '$lib/utils/format-time';
    import { formatBytes } from '$lib/utils/format';

    // The switch rides the shared Save flow (it is a /api/config field); the buttons each ask the
    // server to do a thing now, so they do not.
    let {
        enabled = $bindable<boolean | null>(null),
        onDirty,
    }: {
        enabled?: boolean | null;
        onDirty?: () => void;
    } = $props();

    const status = $derived(updateCheck.status);
    const install = $derived(status?.install ?? null);
    const managed = $derived(status?.disabled === true);
    /** Only a packaged install has a release layout it may replace; everything else reads the reason. */
    const canStage = $derived(install?.can_stage === true);
    // The organization switch answers for every browser, so the toggle shows the server's answer, inert,
    // with the sentence naming it in place of everything a person could click.
    const enabledNow = $derived(!managed && enabled === true);
    const busy = $derived(updateCheck.phase !== 'idle' || !!status?.working);
    const prepared = $derived(status?.prepared_version || '');
    const preparedBytes = $derived(status?.prepared_bytes ?? 0);
    const preparedFiles = $derived(status?.prepared_files ?? updateCheck.lastPrepared?.files ?? 0);
    const staged = $derived(status?.staged_version || '');
    const backup = $derived(status?.backup_path || updateCheck.lastApplied?.backup_path || '');
    const applying = $derived(updateCheck.lastApplied?.restarting === true);
    // The swap landed and this process cannot restart itself; the sentence says how to finish it.
    const restartNote = $derived(updateCheck.lastApplied?.restart_note || '');

    // A version that cannot be compared is not "up to date", so the server's note takes this row.
    const verdict = $derived.by(() => {
        const st = status;
        if (!st) return t('updates.noneKnown');
        if (st.available && st.latest) return t('updates.available', { version: st.latest });
        if (st.note) return st.note;
        if (st.last_error) return st.last_error;
        if (st.latest) return t('updates.upToDate', { version: st.latest });
        return t('updates.noneKnown');
    });

    const checkedAt = $derived(status?.checked_at ? formatRelativeTime(status.checked_at) : '');

    function toggle() {
        enabled = !(enabled === true);
        onDirty?.();
    }
</script>

<div class="section update-section">
    <h3 class="section-label">{t('updates.title')}</h3>

    <div class="update-row">
        <div class="update-row-info">
            <span class="update-row-name">{t('updates.enable')}</span>
            <span id="update-check-desc" class="update-row-desc" data-testid={managed ? 'update-managed' : undefined}>
                {#if managed}
                    {t('updates.managed')}
                {:else}
                    {t('updates.enableDesc', { host: status?.manifest_url ?? '' })}
                {/if}
            </span>
        </div>
        <ToggleSwitch
            checked={enabledNow}
            disabled={managed}
            ariaLabel={t('updates.enable')}
            ariaDescribedby="update-check-desc"
            onChange={toggle}
        />
    </div>

    {#if enabledNow}
        <p class="update-verdict" data-testid="update-verdict">{verdict}</p>
        {#if checkedAt}
            <p class="update-meta">{t('updates.lastChecked', { time: checkedAt })}</p>
        {/if}
        {#if status?.last_error && status.available}
            <!-- A failure beside a still-lit answer: the release seen yesterday is still newer than
                 this one, and saying so is what keeps the dot from looking like a guess. -->
            <p class="update-fault" role="status">{status.last_error}</p>
        {/if}

        {#if prepared}
            <div class="update-card" data-testid="update-prepared">
                <p class="update-card-line">{t('updates.prepared', { version: prepared })}</p>
                {#if preparedBytes > 0}
                    <p class="update-meta">{t('updates.preparedSize', { size: formatBytes(preparedBytes) })}</p>
                {/if}
                {#if preparedFiles > 0}
                    <p class="update-meta">{t('updates.preparedFiles', { files: preparedFiles })}</p>
                {/if}
                {#if updateCheck.lastPrepared?.notes}
                    {#each updateCheck.lastPrepared.notes as note (note)}
                        <p class="update-meta">{note}</p>
                    {/each}
                {/if}
                <button
                    type="button"
                    class="btn-update-apply btn-update-card"
                    data-testid="update-install"
                    onclick={() => void updateCheck.apply()}
                    disabled={busy || !canStage}
                    title={t('updates.installHint')}
                >{updateCheck.phase === 'applying' ? t('updates.installing') : t('updates.install', { version: prepared })}</button>
            </div>
        {/if}

        {#if staged}
            <div class="update-card" data-testid="update-staged">
                <p class="update-card-line">{t('updates.staged', { version: staged })}</p>
                {#if backup}
                    <p class="update-meta">{t('updates.backup', { path: backup })}</p>
                {/if}
                {#if restartNote}
                    <p class="update-meta">{restartNote}</p>
                {/if}
                <p class="update-meta">{t('updates.rollback')}</p>
                {#if updateCheck.lastRestart?.restarting || applying}
                    <p class="update-card-line" role="status">{t('updates.restarting')}</p>
                {:else}
                    <button
                        type="button"
                        class="btn-update-restart btn-update-card"
                        data-testid="update-restart"
                        onclick={() => void updateCheck.restart()}
                        disabled={busy || !canStage}
                        title={t('updates.restartHint')}
                    >{updateCheck.phase === 'restarting' ? t('updates.applying') : t('updates.restart')}</button>
                {/if}
            </div>
        {/if}

        {#if install && !canStage}
            <div class="update-card">
                <p class="update-card-line">{install.reason || t('updates.cannotStage')}</p>
                {#if install.kind === 'container'}
                    <p class="update-command"><code>{t('updates.pullCommand')}</code></p>
                {/if}
            </div>
        {:else}
            <div class="update-actions">
                <button
                    type="button"
                    class="btn-update-check"
                    data-testid="update-check-now"
                    onclick={() => void updateCheck.checkNow()}
                    disabled={busy}
                    title={t('updates.checkHint')}
                >{updateCheck.phase === 'checking' ? t('settings.checking') : t('updates.check')}</button>
                <button
                    type="button"
                    class="btn-update-prepare"
                    data-testid="update-download"
                    onclick={() => void updateCheck.prepare()}
                    disabled={busy || !status?.available || !!prepared}
                    title={t('updates.downloadHint')}
                >{updateCheck.phase === 'preparing' ? t('updates.downloading') : t('updates.download')}</button>
            </div>
        {/if}

        {#if updateCheck.fault}
            <p class="update-fault" role="status">{updateCheck.fault}</p>
        {/if}
    {:else if !managed}
        <p class="update-off" role="status">{t('updates.off')}</p>
    {/if}
</div>

<style>
    .update-section .section-label {
        margin-bottom: 8px;
    }

    .update-row {
        display: flex;
        align-items: flex-start;
        justify-content: space-between;
        gap: 16px;
    }

    .update-row-info {
        display: flex;
        flex-direction: column;
        gap: 2px;
    }

    .update-row-name {
        font-size: 13px;
        color: var(--color-text-primary);
    }

    .update-row-desc {
        font-size: 12px;
        color: var(--color-text-tertiary);
        max-width: 52ch;
    }

    .update-verdict {
        margin: 10px 0 0;
        font-size: 13px;
        color: var(--color-text-primary);
    }

    .update-off,
    .update-meta {
        margin: 4px 0 0;
        font-size: 12px;
        color: var(--color-text-tertiary);
    }

    .update-fault {
        margin: 8px 0 0;
        font-size: 12px;
        color: var(--color-status-red);
        overflow-wrap: anywhere;
    }

    .update-card {
        margin-top: 10px;
        padding: 10px 12px;
        border: 1px solid var(--color-border);
        border-radius: 6px;
        background: var(--color-surface-1);
    }

    .update-card-line {
        margin: 0;
        font-size: 13px;
        color: var(--color-text-primary);
        overflow-wrap: anywhere;
    }

    .update-command {
        margin: 6px 0 0;
        font-size: 12px;
    }

    .update-command code {
        font-family: var(--font-mono);
        color: var(--color-text-primary);
        overflow-wrap: anywhere;
    }

    .update-actions {
        display: flex;
        gap: 8px;
        margin-top: 10px;
    }

    .btn-update-check,
    .btn-update-prepare,
    .btn-update-apply,
    .btn-update-restart {
        padding: 5px 12px;
        font-size: 12px;
        border-radius: 6px;
        border: 1px solid var(--color-border);
        background: var(--color-surface-2);
        color: var(--color-text-primary);
        cursor: pointer;
    }

    .btn-update-card {
        display: block;
        margin-top: 8px;
    }

    .btn-update-prepare,
    .btn-update-apply,
    .btn-update-restart {
        border-color: var(--color-accent);
        color: var(--color-accent);
    }

    .btn-update-check:hover:not(:disabled),
    .btn-update-prepare:hover:not(:disabled),
    .btn-update-apply:hover:not(:disabled),
    .btn-update-restart:hover:not(:disabled) {
        background: var(--color-surface-3);
    }

    .btn-update-check:disabled,
    .btn-update-prepare:disabled,
    .btn-update-apply:disabled,
    .btn-update-restart:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }
</style>
