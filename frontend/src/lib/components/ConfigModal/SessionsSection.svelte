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
    import { formatAgeAndTooltip } from '$lib/utils/format-time';
    import { t } from '$lib/i18n';

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
            cancelText: t('app.cancel'),
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
            cancelText: t('app.cancel'),
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

<section class="settings-section sessions-section">
    <div class="settings-section-header">
        <h3 class="settings-section-title">{t('settings.sessions.title')}</h3>
        <div class="settings-section-actions">
            <button
                type="button"
                class="settings-btn-small"
                onclick={loadSessions}
                disabled={loading || revokingOthers}
                aria-label={t('settings.sessions.reloadTitle')}
                title={t('settings.sessions.reloadTitle')}
            >{loading ? t('settings.sessions.loading') : t('settings.sessions.refresh')}</button>
            <button
                type="button"
                class="settings-btn-small settings-btn-danger"
                onclick={handleRevokeOthers}
                disabled={loading || revokingOthers || otherCount === 0}
                title={t('settings.sessions.revokeAllHint')}
            >{revokingOthers ? t('settings.sessions.revoking') : t('settings.sessions.revokeAllOther')}</button>
        </div>
    </div>

    {#if error}
        <div class="settings-list-error" role="alert">{error}</div>
    {/if}

    {#if loading && sessions.length === 0}
        <div class="settings-list-status" role="status">{t('settings.sessions.loadingList')}</div>
    {:else if sessions.length === 0 && !error}
        <div class="settings-list-status" role="status">{t('settings.sessions.none')}</div>
    {:else}
        {#each rows as session (session.id)}
            {@const created = formatAgeAndTooltip(session.created_at, '\u2014', t('settings.sessions.unknownTime'))}
            {@const seen = formatAgeAndTooltip(session.last_seen, '\u2014', t('settings.sessions.unknownTime'))}
            <div class="settings-row" class:session-current={session.current} data-session-id={session.id}>
                <div class="settings-row-info">
                    <div class="session-headline">
                        <span class="settings-row-label" title={session.user_agent || t('settings.sessions.unknownDevice')}>
                            {session.user_agent || t('settings.sessions.unknownDevice')}
                        </span>
                        {#if session.current}
                            <span class="session-badge">{t('settings.sessions.thisDevice')}</span>
                        {/if}
                    </div>
                    <div class="settings-row-meta">
                        <span class="session-meta-item" title={t('settings.sessions.signedInAt', { time: created.tooltip })}>
                            <span class="settings-row-meta-key">{t('settings.sessions.created')}</span> {created.display}
                        </span>
                        <span class="session-meta-item" title={t('settings.sessions.lastSeenAt', { time: seen.tooltip })}>
                            <span class="settings-row-meta-key">{t('settings.sessions.lastSeen')}</span> {seen.display}
                        </span>
                        <span class="session-meta-item">
                            <span class="settings-row-meta-key">{t('settings.sessions.ip')}</span> {session.ip || '\u2014'}
                        </span>
                    </div>
                </div>
                <button
                    type="button"
                    class="settings-btn-small settings-btn-danger session-revoke"
                    onclick={() => handleRevoke(session)}
                    disabled={revokingId === session.id || revokingOthers}
                    aria-label={t('settings.sessions.revokeSessionAria', { id: session.id })}
                    title={session.current ? t('settings.sessions.revokeDeviceHint') : t('settings.sessions.revokeSessionHint')}
                >{revokingId === session.id ? t('settings.sessions.revoking') : t('settings.sessions.revoke')}</button>
            </div>
        {/each}
        {#if otherCount === 0}
            <div class="settings-list-status" role="status">{t('settings.sessions.none')}</div>
        {/if}
    {/if}
</section>

<style>
    @import '$lib/styles/settings-section.css';

    .sessions-section {
        gap: 10px;
    }

    .session-current {
        border-color: var(--color-accent);
    }
    .session-headline {
        display: flex;
        align-items: center;
        gap: 8px;
        min-width: 0;
    }
    .session-badge {
        flex-shrink: 0;
        font-size: var(--text-2xs);
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.04em;
        padding: 2px 6px;
        border-radius: var(--radius-sm);
        background: var(--color-accent);
        color: var(--color-btn-text);
    }
</style>
