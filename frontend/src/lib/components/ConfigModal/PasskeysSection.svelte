<script lang="ts">
    /**
     * Devices and passkeys section of Settings → About. Lists every credential this install will sign in
     * (`GET /api/auth/passkeys`), renames and revokes them, and mints the one-time pairing code that lets
     * one more device register (`POST /api/auth/pairing`).
     *
     * Mutates immediately, so it sits outside the modal's Save/Cancel flow like the two sections under it,
     * and it fetches when the About tab mounts. Two things are only ever held here: the code, which the
     * server keeps in memory until it is spent, and the pairing URL, whose fragment holds that same code —
     * which is why the URL is copyable and never fetched from here.
     */
    import type { PairingCode, PasskeyInfo } from '$lib/types';
    import { listPasskeys, mintPairingCode, renamePasskey, revokePairingCodes, revokePasskey } from '$lib/api';
    import { dialogStore } from '$lib/stores';
    import { formatDateTime, formatElapsed, formatRelativeTime } from '$lib/utils/format-time';
    import { DEVICE_NAME_LIMIT } from '$lib/utils/login-flow';
    import { t } from '$lib/i18n';

    let passkeys = $state<PasskeyInfo[]>([]);
    // Starts true so the first paint is the loading state, not a flash of the empty state.
    let loading = $state(true);
    let error = $state('');
    /** id of the row whose Revoke button is in flight (disables the row). */
    let revokingId = $state('');
    /** The row being renamed, and what is typed in its field. */
    let editingId = $state('');
    let editName = $state('');
    let savingId = $state('');
    /** The outstanding code, and the instant the countdown counts against. */
    let pairing = $state<PairingCode | null>(null);
    let minting = $state(false);
    /** What to call the device being paired; it travels in the pairing link, so a code typed by hand
     *  arrives unnamed. */
    let mintName = $state('');
    let revokingCodes = $state(false);
    let copied = $state(false);
    let now = $state(Date.now());

    /** Oldest first: the first passkey paired is the one a person recognises as "my laptop". */
    const rows = $derived.by(() =>
        [...passkeys].sort((a, b) => (a.created_at || '').localeCompare(b.created_at || ''))
    );

    /** Time left on the code; 0 once it has passed, so one comparison decides both the label and the
     *  fact that nothing is waiting any more. */
    const secondsLeft = $derived(Math.max(0, Math.floor((Date.parse(pairing?.expires_at ?? '') - now) / 1000)));
    const expired = $derived(!!pairing && secondsLeft <= 0);

    /** Absolute timestamp for a row's `title` tooltip. */
    function absolute(iso: string): string {
        return iso ? formatDateTime(iso) : t('settings.passkeys.unknownTime');
    }

    /** Relative display text; a device that has never signed in says so rather than showing a date. */
    function relative(iso: string): string {
        if (!iso) return t('settings.passkeys.neverUsed');
        return formatRelativeTime(iso) || absolute(iso);
    }

    async function loadPasskeys() {
        loading = true;
        error = '';
        try {
            passkeys = await listPasskeys();
        } catch (e: any) {
            error = e?.message || t('settings.passkeys.loadFailed');
            passkeys = [];
        } finally {
            loading = false;
        }
    }

    function startRename(row: PasskeyInfo) {
        editingId = row.id;
        editName = row.name;
    }

    function cancelRename(): void {
        editingId = '';
        editName = '';
    }

    async function saveRename(row: PasskeyInfo) {
        const name = editName.trim();
        if (!name || name === row.name) {
            cancelRename();
            return;
        }
        savingId = row.id;
        error = '';
        try {
            await renamePasskey(row.id, name);
            passkeys = passkeys.map(entry => (entry.id === row.id ? { ...entry, name } : entry));
            cancelRename();
        } catch (e: any) {
            error = e?.message || t('api.renamePasskey');
        } finally {
            savingId = '';
        }
    }

    async function handleRevoke(row: PasskeyInfo) {
        const confirmed = await dialogStore.showConfirm({
            title: t('settings.passkeys.revokeTitle'),
            message: t('settings.passkeys.revokeMessage'),
            confirmText: t('settings.passkeys.revoke'),
            cancelText: t('settings.cancel'),
            variant: 'destructive',
        });
        if (!confirmed) return;

        revokingId = row.id;
        error = '';
        try {
            await revokePasskey(row.id);
            passkeys = passkeys.filter(entry => entry.id !== row.id);
            dialogStore.showToast(t('settings.passkeys.revokedToast'), 'success');
        } catch (e: any) {
            // The last passkey is refused with the server's reason: this is the one action here that
            // cannot be allowed to succeed, because a GUI that could reach zero credentials can lock a
            // running server out of itself.
            error = e?.message || t('api.revokePasskey');
        } finally {
            revokingId = '';
        }
    }

    async function handleMint() {
        minting = true;
        error = '';
        try {
            pairing = await mintPairingCode(mintName.trim());
            copied = false;
            now = Date.now();
        } catch (e: any) {
            error = e?.message || t('api.mintPairingCode');
        } finally {
            minting = false;
        }
    }

    async function handleRevokeCodes() {
        const confirmed = await dialogStore.showConfirm({
            title: t('settings.passkeys.revokeCodesTitle'),
            message: t('settings.passkeys.revokeCodesMessage'),
            confirmText: t('settings.passkeys.revokeCodes'),
            cancelText: t('settings.cancel'),
            variant: 'destructive',
        });
        if (!confirmed) return;

        revokingCodes = true;
        error = '';
        try {
            await revokePairingCodes();
            pairing = null;
            dialogStore.showToast(t('settings.passkeys.codesRevokedToast'), 'success');
        } catch (e: any) {
            error = e?.message || t('api.revokePairingCodes');
        } finally {
            revokingCodes = false;
        }
    }

    /** Copy the pairing link, code included: scanning the picture is the quick path, typing the code is
     *  the one that works when the two machines are on different networks. */
    async function handleCopy() {
        if (!pairing) return;
        try {
            await navigator.clipboard.writeText(pairing.url);
            copied = true;
        } catch {
            // A denied clipboard leaves the value on screen to select by hand.
            dialogStore.showToast(t('settings.passkeys.copyFailed'), 'error');
        }
    }

    // Fetch on mount — that is, when the About tab opens, not when the Settings modal mounts.
    $effect(() => {
        void loadPasskeys();
    });

    /** The countdown's clock. Only the seconds the person is watching keep a timer: with no code on
     *  screen nothing is counting down, and an interval that outlives the code re-renders the section
     *  once a second for nothing. */
    $effect(() => {
        if (!pairing) return;
        const tick = setInterval(() => { now = Date.now(); }, 1000);
        return () => clearInterval(tick);
    });
</script>

<section class="settings-section passkeys-section">
    <div class="settings-section-header">
        <h3 class="settings-section-title">{t('settings.passkeys.title')}</h3>
        <div class="settings-section-actions">
            <button
                type="button"
                class="settings-btn-small"
                onclick={loadPasskeys}
                disabled={loading || minting}
                aria-label={t('settings.passkeys.reloadTitle')}
                title={t('settings.passkeys.reloadTitle')}
            >{loading ? t('settings.passkeys.loading') : t('settings.passkeys.refresh')}</button>
            <button
                type="button"
                class="settings-btn-small settings-btn-danger"
                onclick={handleRevokeCodes}
                disabled={revokingCodes || !pairing}
                title={t('settings.passkeys.revokeCodesHint')}
            >{revokingCodes ? t('settings.passkeys.revokingCodes') : t('settings.passkeys.revokeCodes')}</button>
        </div>
    </div>

    <p class="settings-note">{t('settings.passkeys.help')}</p>

    <div class="settings-create">
        <input
            type="text"
            class="settings-create-input"
            bind:value={mintName}
            maxlength={DEVICE_NAME_LIMIT}
            placeholder={t('settings.passkeys.mintNamePlaceholder')}
            aria-label={t('settings.passkeys.mintNameAria')}
            disabled={minting || revokingCodes}
            onkeydown={(e) => { if (e.key === 'Enter') void handleMint(); }}
        />
        <button
            type="button"
            class="btn btn-primary"
            onclick={handleMint}
            disabled={loading || minting || revokingCodes}
            title={t('settings.passkeys.pairHint')}
        >{minting ? t('settings.passkeys.minting') : t('settings.passkeys.pair')}</button>
    </div>
    <p class="settings-note">{t('settings.passkeys.mintNameHelp')}</p>

    {#if pairing}
        <div class="settings-revealed passkeys-pairing" class:passkeys-pairing-expired={expired} role="status">
            <img class="passkeys-qr" src={pairing.qr_png} width="132" height="132" alt={t('settings.passkeys.qrAlt')} />
            <div class="passkeys-pairing-body">
                <div class="settings-revealed-note">{t('settings.passkeys.pairTitle')}</div>
                <code class="passkeys-code" title={pairing.code}>{pairing.code}</code>
                <div class="settings-revealed-row">
                    <code class="settings-row-value passkeys-url" title={pairing.url}>{pairing.url}</code>
                    <button type="button" class="settings-btn-small" onclick={handleCopy}>
                        {copied ? t('settings.passkeys.copied') : t('settings.passkeys.copy')}
                    </button>
                </div>
                {#if expired}
                    <div class="passkeys-pairing-expired-note">{t('settings.passkeys.expired')}</div>
                {:else}
                    <div class="passkeys-pairing-countdown">
                        {t('settings.passkeys.expiresIn', { time: formatElapsed(secondsLeft * 1000) })}
                    </div>
                {/if}
            </div>
        </div>
    {/if}

    {#if error}
        <div class="settings-list-error" role="alert">{error}</div>
    {/if}

    {#if loading && passkeys.length === 0}
        <div class="settings-list-status" role="status">{t('settings.passkeys.loadingList')}</div>
    {:else if passkeys.length === 0 && !error}
        <div class="settings-list-status" role="status">{t('settings.passkeys.none')}</div>
    {:else}
        {#each rows as row (row.id)}
            <div class="settings-row" data-passkey-id={row.id}>
                <div class="settings-row-info">
                    {#if editingId === row.id}
                        <input
                            class="passkeys-name-input"
                            type="text"
                            bind:value={editName}
                            maxlength={64}
                            aria-label={t('settings.passkeys.nameAria')}
                            disabled={savingId === row.id}
                            onkeydown={(e) => {
                                if (e.key === 'Enter') void saveRename(row);
                                if (e.key === 'Escape') cancelRename();
                            }}
                            onblur={() => void saveRename(row)}
                        />
                    {:else}
                        <button
                            type="button"
                            class="settings-row-label passkeys-name"
                            title={t('settings.passkeys.renameHint')}
                            onclick={() => startRename(row)}
                        >{row.name}</button>
                    {/if}
                    <div class="settings-row-meta">
                        <span class="passkeys-meta-item" title={absolute(row.created_at)}>
                            <span class="settings-row-meta-key">{t('settings.passkeys.created')}</span> {relative(row.created_at)}
                        </span>
                        <span class="passkeys-meta-item" title={row.last_used ? absolute(row.last_used) : ''}>
                            <span class="settings-row-meta-key">{t('settings.passkeys.lastUsed')}</span> {relative(row.last_used)}
                        </span>
                    </div>
                </div>
                <button
                    type="button"
                    class="settings-btn-small settings-btn-danger passkey-revoke"
                    onclick={() => handleRevoke(row)}
                    disabled={revokingId === row.id || savingId === row.id}
                    aria-label={t('settings.passkeys.revokePasskeyAria', { name: row.name })}
                    title={t('settings.passkeys.revokeHint')}
                >{revokingId === row.id ? t('settings.passkeys.revoking') : t('settings.passkeys.revoke')}</button>
            </div>
        {/each}
    {/if}
</section>

<style>
    @import '$lib/styles/settings-section.css';

    .passkeys-section {
        gap: 10px;
    }
    /* The label's own shape comes from `.settings-row-label`; this is only what makes it a field. */
    .passkeys-name {
        align-self: flex-start;
        background: none;
        border: none;
        border-bottom: 1px dashed var(--color-border);
        padding: 0 0 1px;
        font-family: inherit;
        text-align: left;
        max-width: 100%;
        cursor: text;
    }
    .passkeys-name-input {
        width: 100%;
        padding: 4px 8px;
        border: 1px solid var(--color-accent);
        border-radius: var(--radius-sm);
        background: var(--color-surface-2);
        color: var(--color-text-primary);
        font-family: inherit;
        font-size: var(--text-sm);
    }

    /* The revealed box lies down rather than standing up: the picture and the code are two ways of
       handing the same link to a second device, and they read as one thing side by side. */
    .passkeys-pairing {
        flex-direction: row;
        gap: 12px;
        align-items: flex-start;
    }
    .passkeys-pairing-expired {
        border-color: var(--color-border);
    }
    .passkeys-qr {
        flex-shrink: 0;
        border-radius: var(--radius-sm);
    }
    .passkeys-pairing-body {
        display: flex;
        flex-direction: column;
        gap: 6px;
        min-width: 0;
    }
    .passkeys-code {
        align-self: flex-start;
        font-family: var(--font-mono, 'SF Mono', 'Cascadia Code', 'Fira Code', monospace);
        font-size: var(--text-lg);
        letter-spacing: 0.08em;
        color: var(--color-text-primary);
    }
    .passkeys-pairing .settings-revealed-row {
        min-width: 0;
    }
    /* `.settings-row-value` truncates it rather than wrapping it: a URL broken over a line is a broken
       link when it is pasted. */
    .passkeys-url {
        color: var(--color-text-secondary);
    }
    .passkeys-pairing-countdown,
    .passkeys-pairing-expired-note {
        font-size: var(--text-xs);
        color: var(--color-text-tertiary);
    }

</style>
