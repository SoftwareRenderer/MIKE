<script lang="ts">
    /**
     * Sessions section of Settings → About. Lists every live login session
     * (GET /api/sessions) and revokes on click — deliberately outside the modal's
     * Save/Cancel flow, so it never touches the dirty state or save payload (like
     * the PWA install button above it). Rendered inside the `about` tab block, so
     * it fetches when that tab opens.
     */
    import type { SessionInfo } from '$lib/types';
    import { listSessions, revokeSession, revokeOtherSessions } from '$lib/api';
    import { dialogStore } from '$lib/stores';
    import { formatRelativeTime } from '$lib/utils/format-time';
    import { t } from '$lib/i18n';
    import { activeLocale } from '$lib/i18n/runtime.svelte';

    /** Sign-out path owned by the modal (App's logout handler). */
    let { onSignOut }: { onSignOut?: () => void | Promise<void> } = $props();

    let sessions = $state<SessionInfo[]>([]);
    // Starts true so the first paint is the loading state, not a flash of the
    // empty state (the mount effect fetches immediately).
    let loading = $state(true);
    let error = $state('');
    /** id of the session whose Revoke button is in flight (disables the row). */
    let revokingId = $state('');
    let revokingOthers = $state(false);

    /** Rows newest first, with the caller's own session pinned on top. */
    const rows = $derived.by(() => {
        const sorted = [...sessions].sort((a, b) => (b.created_at || '').localeCompare(a.created_at || ''));
        return sorted.sort((a, b) => Number(b.current) - Number(a.current));
    });
    const otherCount = $derived(sessions.filter(s => !s.current).length);

    /** Absolute timestamp for a row's `title` tooltip. */
    function absolute(iso: string): string {
        if (!iso) return t('settings.sessions.unknownTime');
        const date = new Date(iso);
        return isNaN(date.getTime()) ? iso : date.toLocaleString(activeLocale());
    }

    /** Relative display text; empty timestamps render as an em dash. */
    function relative(iso: string): string {
        if (!iso) return '\u2014';
        return formatRelativeTime(iso) || absolute(iso);
    }

    async function loadSessions() {
        loading = true;
        error = '';
        try {
            sessions = await listSessions();
        } catch (e: any) {
            error = e?.message || t('settings.sessions.loadFailed');
            sessions = [];
        } finally {
            loading = false;
        }
    }

    async function handleRevoke(session: SessionInfo) {
        const isCurrent = session.current;
        const confirmed = await dialogStore.showConfirm({
            title: isCurrent ? t('settings.sessions.revokeThisDevice') : t('settings.sessions.revokeThisSession'),
            message: isCurrent
                ? t('settings.sessions.revokeThisDeviceMessage')
                : t('settings.sessions.revokeOtherMessage'),
            confirmText: isCurrent ? t('settings.signOut') : t('settings.sessions.revoke'),
            cancelText: t('settings.cancel'),
            variant: 'destructive',
        });
        if (!confirmed) return;

        revokingId = session.id;
        error = '';
        try {
            await revokeSession(session.id);
            if (isCurrent) {
                // The session is gone and every later request would 401. Hand over
                // to the modal's sign-out path instead of refreshing.
                await onSignOut?.();
                return;
            }
            sessions = sessions.filter(s => s.id !== session.id);
            dialogStore.showToast(t('settings.sessions.revokedToast'), 'success');
        } catch (e: any) {
            error = e?.message || t('settings.sessions.revokeFailed');
        } finally {
            revokingId = '';
        }
    }

    async function handleRevokeOthers() {
        const count = otherCount;
        const confirmed = await dialogStore.showConfirm({
            title: t('settings.sessions.revokeAllTitle'),
            message: t('settings.sessions.revokeAllMessage', { count }),
            confirmText: t('settings.sessions.revokeAll'),
            cancelText: t('settings.cancel'),
            variant: 'destructive',
        });
        if (!confirmed) return;

        revokingOthers = true;
        error = '';
        try {
            const { revoked } = await revokeOtherSessions();
            dialogStore.showToast(t('settings.sessions.revokedCountToast', { count: revoked }), 'success');
            await loadSessions();
        } catch (e: any) {
            error = e?.message || t('settings.sessions.revokeAllFailed');
        } finally {
            revokingOthers = false;
        }
    }

    // Fetch on mount — that is, when the About tab opens, not when the Settings
    // modal mounts.
    $effect(() => {
        void loadSessions();
    });
</script>

<section class="section sessions-section">
    <div class="section-header">
        <h3 class="section-label">{t('settings.sessions.title')}</h3>
        <div class="section-actions">
            <button
                type="button"
                class="btn-small"
                onclick={loadSessions}
                disabled={loading || revokingOthers}
                aria-label="refresh-sessions"
                title={t('settings.sessions.reloadTitle')}
            >{loading ? t('settings.sessions.loading') : t('settings.sessions.refresh')}</button>
            <button
                type="button"
                class="btn-small btn-danger"
                onclick={handleRevokeOthers}
                disabled={loading || revokingOthers || otherCount === 0}
                aria-label="revoke-all-other-sessions"
                title={t('settings.sessions.revokeAllHint')}
            >{revokingOthers ? t('settings.sessions.revoking') : t('settings.sessions.revokeAllOther')}</button>
        </div>
    </div>

    {#if error}
        <div class="sessions-error" role="alert">{error}</div>
    {/if}

    {#if loading && sessions.length === 0}
        <div class="sessions-status" role="status">{t('settings.sessions.loadingList')}</div>
    {:else if sessions.length === 0 && !error}
        <div class="sessions-status" role="status">{t('settings.sessions.none')}</div>
    {:else}
        {#each rows as session (session.id)}
            <div class="session-row" class:session-row-current={session.current} data-session-id={session.id}>
                <div class="session-info">
                    <div class="session-headline">
                        <span class="session-agent" title={session.user_agent || t('settings.sessions.unknownDevice')}>
                            {session.user_agent || t('settings.sessions.unknownDevice')}
                        </span>
                        {#if session.current}
                            <span class="session-badge">{t('settings.sessions.thisDevice')}</span>
                        {/if}
                    </div>
                    <div class="session-meta">
                        <span class="session-meta-item" title={t('settings.sessions.signedInAt', { time: absolute(session.created_at) })}>
                            <span class="session-meta-key">{t('settings.sessions.created')}</span> {relative(session.created_at)}
                        </span>
                        <span class="session-meta-item" title={t('settings.sessions.lastSeenAt', { time: absolute(session.last_seen) })}>
                            <span class="session-meta-key">{t('settings.sessions.lastSeen')}</span> {relative(session.last_seen)}
                        </span>
                        <span class="session-meta-item">
                            <span class="session-meta-key">{t('settings.sessions.ip')}</span> {session.ip || '\u2014'}
                        </span>
                    </div>
                </div>
                <button
                    type="button"
                    class="btn-small btn-danger session-revoke"
                    onclick={() => handleRevoke(session)}
                    disabled={revokingId === session.id || revokingOthers}
                    aria-label={`revoke-session-${session.id}`}
                    title={session.current ? t('settings.sessions.revokeDeviceHint') : t('settings.sessions.revokeSessionHint')}
                >{revokingId === session.id ? t('settings.sessions.revoking') : t('settings.sessions.revoke')}</button>
            </div>
        {/each}
        {#if otherCount === 0}
            <div class="sessions-status" role="status">{t('settings.sessions.none')}</div>
        {/if}
    {/if}
</section>

<style>
    /* Matches the About tab's section chrome (ConfigModal.svelte). */
    .section {
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        padding: 16px;
        display: flex;
        flex-direction: column;
        gap: 12px;
    }
    .section-label {
        margin: 0;
        font-size: 0.875rem;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.05em;
        color: var(--color-text-secondary);
    }
    .section-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 8px;
        flex-wrap: wrap;
    }
    .section-actions {
        display: flex;
        gap: 8px;
    }

    .sessions-section {
        gap: 10px;
    }

    .session-row {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 12px;
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        background: var(--color-surface-2);
        padding: 10px 12px;
    }
    .session-row-current {
        border-color: var(--color-accent);
    }
    .session-info {
        display: flex;
        flex-direction: column;
        gap: 4px;
        min-width: 0;
    }
    .session-headline {
        display: flex;
        align-items: center;
        gap: 8px;
        min-width: 0;
    }
    /* Ellipsize the User-Agent; the full string stays in the title tooltip. */
    .session-agent {
        font-size: 0.8125rem;
        font-weight: 500;
        color: var(--color-text-primary);
        min-width: 0;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }
    .session-badge {
        flex-shrink: 0;
        font-size: 0.6875rem;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.04em;
        padding: 2px 6px;
        border-radius: var(--radius-sm);
        background: var(--color-accent);
        color: white;
    }
    .session-meta {
        display: flex;
        flex-wrap: wrap;
        gap: 4px 14px;
        font-family: var(--font-mono, 'SF Mono', 'Cascadia Code', 'Fira Code', monospace);
        font-size: 0.75rem;
        color: var(--color-text-secondary);
    }
    .session-meta-key {
        color: var(--color-text-tertiary);
    }

    .btn-small {
        padding: 6px 12px;
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        background: var(--color-surface-3);
        color: var(--color-text-primary);
        font-size: 0.8125rem;
        font-weight: 500;
        font-family: inherit;
        cursor: pointer;
        white-space: nowrap;
        flex-shrink: 0;
    }
    .btn-small:hover:not(:disabled) {
        background: var(--color-surface-4);
    }
    .btn-small:disabled {
        opacity: 0.55;
        cursor: default;
    }
    .btn-danger {
        color: var(--color-status-red);
        border-color: var(--color-status-red);
    }

    .sessions-status {
        font-size: 0.8125rem;
        color: var(--color-text-tertiary);
    }
    .sessions-error {
        font-size: 0.8125rem;
        font-weight: 500;
        color: var(--color-status-red);
    }
</style>
