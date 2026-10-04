<script lang="ts">
    /**
     * API tokens section of Settings → About. Issues and revokes the bearer credentials a headless
     * client presents (`Authorization: Bearer …`) on the task and project routes they cover.
     *
     * Mutates immediately, so it sits outside the modal's Save/Cancel flow like the Sessions section
     * below it, and it fetches when the About tab mounts. The issued secret lives in this component
     * only: the server keeps its digest, so nothing here can show it again after the modal closes.
     */
    import type { APITokenInfo } from '$lib/types';
    import { listAPITokens, createAPIToken, revokeAPIToken } from '$lib/api';
    import { dialogStore } from '$lib/stores';
    import { formatDateTime, formatRelativeTime } from '$lib/utils/format-time';
    import { t } from '$lib/i18n';

    let tokens = $state<APITokenInfo[]>([]);
    // Starts true so the first paint is the loading state, not a flash of the empty state.
    let loading = $state(true);
    let error = $state('');
    let label = $state('');
    let creating = $state(false);
    /** id of the token whose Revoke button is in flight (disables the row). */
    let revokingId = $state('');
    /** The secret this screen just issued, and the confirmation that it was copied. */
    let issued = $state<{ id: string; token: string } | null>(null);
    let copied = $state(false);

    const rows = $derived.by(() =>
        [...tokens].sort((a, b) => (b.created_at || '').localeCompare(a.created_at || ''))
    );

    /** Absolute timestamp for a row's `title` tooltip. */
    function absolute(iso: string): string {
        return iso ? formatDateTime(iso) : t('settings.tokens.neverUsed');
    }

    /** Relative display text; a token that has never been used says so rather than showing a date. */
    function relative(iso: string): string {
        if (!iso) return t('settings.tokens.neverUsed');
        return formatRelativeTime(iso) || absolute(iso);
    }

    async function loadTokens() {
        loading = true;
        error = '';
        try {
            tokens = await listAPITokens();
        } catch (e: any) {
            error = e?.message || t('settings.tokens.loadFailed');
            tokens = [];
        } finally {
            loading = false;
        }
    }

    async function handleCreate() {
        const name = label.trim();
        if (!name) {
            error = t('settings.tokens.labelRequired');
            return;
        }
        creating = true;
        error = '';
        try {
            const created = await createAPIToken(name);
            tokens = [created, ...tokens];
            issued = { id: created.id, token: created.token };
            copied = false;
            label = '';
        } catch (e: any) {
            error = e?.message || t('settings.tokens.createFailed');
        } finally {
            creating = false;
        }
    }

    async function handleCopy() {
        if (!issued) return;
        try {
            await navigator.clipboard.writeText(issued.token);
            copied = true;
        } catch {
            // A denied or unavailable clipboard leaves the value on screen to select by hand.
            dialogStore.showToast(t('settings.tokens.copy'), 'error');
        }
    }

    async function handleRevoke(token: APITokenInfo) {
        const confirmed = await dialogStore.showConfirm({
            title: t('settings.tokens.revokeTitle'),
            message: t('settings.tokens.revokeMessage'),
            confirmText: t('settings.tokens.revoke'),
            cancelText: t('settings.cancel'),
            variant: 'destructive',
        });
        if (!confirmed) return;

        revokingId = token.id;
        error = '';
        try {
            await revokeAPIToken(token.id);
            tokens = tokens.filter((row) => row.id !== token.id);
            // A revocation retires the secret on screen too: showing a copy of a dead credential
            // would be an invitation to paste it somewhere.
            if (issued?.id === token.id) issued = null;
            dialogStore.showToast(t('settings.tokens.revokedToast'), 'success');
        } catch (e: any) {
            error = e?.message || t('settings.tokens.revokeFailed');
        } finally {
            revokingId = '';
        }
    }

    // Fetch on mount — that is, when the About tab opens, not when the Settings modal mounts.
    $effect(() => {
        void loadTokens();
    });
</script>

<section class="settings-section tokens-section">
    <div class="settings-section-header">
        <h3 class="settings-section-title">{t('settings.tokens.title')}</h3>
        <div class="settings-section-actions">
            <button
                type="button"
                class="settings-btn-small"
                onclick={loadTokens}
                disabled={loading || creating}
                aria-label={t('settings.tokens.reloadTitle')}
                title={t('settings.tokens.reloadTitle')}
            >{loading ? t('settings.tokens.loading') : t('settings.tokens.refresh')}</button>
        </div>
    </div>

    <p class="settings-note">{t('settings.tokens.help')}</p>

    <div class="tokens-create">
        <input
            type="text"
            class="tokens-label"
            bind:value={label}
            maxlength={80}
            placeholder={t('settings.tokens.labelPlaceholder')}
            aria-label={t('settings.tokens.labelAria')}
            disabled={creating}
            onkeydown={(e) => { if (e.key === 'Enter') void handleCreate(); }}
        />
        <button
            type="button"
            class="btn btn-primary"
            onclick={handleCreate}
            disabled={creating || loading}
        >{creating ? t('settings.tokens.creating') : t('settings.tokens.create')}</button>
    </div>

    {#if issued}
        <div class="settings-revealed" role="status">
            <div class="settings-revealed-note">{t('settings.tokens.createdTitle')}</div>
            <div class="settings-revealed-row">
                <code class="settings-row-value tokens-secret" title={issued.token}>{issued.token}</code>
                <button type="button" class="settings-btn-small" onclick={handleCopy}>{copied ? t('settings.tokens.copied') : t('settings.tokens.copy')}</button>
            </div>
        </div>
    {/if}

    {#if error}
        <div class="settings-list-error" role="alert">{error}</div>
    {/if}

    {#if loading && tokens.length === 0}
        <div class="settings-list-status" role="status">{t('settings.tokens.loadingList')}</div>
    {:else if tokens.length === 0 && !error}
        <div class="settings-list-status" role="status">{t('settings.tokens.none')}</div>
    {:else}
        {#each rows as token (token.id)}
            <div class="settings-row" data-token-id={token.id}>
                <div class="settings-row-info">
                    <div class="settings-row-label" title={token.label}>{token.label}</div>
                    <div class="settings-row-meta">
                        <span class="token-meta-item" title={absolute(token.created_at)}>
                            <span class="settings-row-meta-key">{t('settings.tokens.created')}</span> {relative(token.created_at)}
                        </span>
                        <span class="token-meta-item" title={token.last_used_at ? absolute(token.last_used_at) : ''}>
                            <span class="settings-row-meta-key">{t('settings.tokens.lastUsed')}</span> {relative(token.last_used_at)}
                        </span>
                    </div>
                </div>
                <button
                    type="button"
                    class="settings-btn-small settings-btn-danger token-revoke"
                    onclick={() => handleRevoke(token)}
                    disabled={revokingId === token.id}
                    aria-label={t('settings.tokens.revokeTokenAria', { id: token.id })}
                    title={t('settings.tokens.revokeHint')}
                >{revokingId === token.id ? t('settings.tokens.revoking') : t('settings.tokens.revoke')}</button>
            </div>
        {/each}
    {/if}
</section>

<style>
    @import '$lib/styles/settings-section.css';

    .tokens-section {
        gap: 10px;
    }
    .tokens-create {
        display: flex;
        gap: 8px;
        align-items: center;
    }
    .tokens-label {
        flex: 1;
        min-width: 0;
        padding: 6px 10px;
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        background: var(--color-surface-2);
        color: var(--color-text-primary);
        font-size: var(--text-sm);
        font-family: inherit;
    }

    /* The secret is selectable but truncated by `.settings-row-value`: a wrapped token pasted from a
       line break is a broken credential. */
    .tokens-secret {
        color: var(--color-text-primary);
    }

</style>
