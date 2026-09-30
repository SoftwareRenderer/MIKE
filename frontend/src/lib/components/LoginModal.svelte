<script lang="ts">
    import { onMount } from 'svelte';
    import { login } from '$lib/api';
    import { t } from '$lib/i18n';
    import BaseModal from './BaseModal.svelte';

    let { onClose }: { onClose: () => void | Promise<void> } = $props();
    let password = $state('');
    let error = $state('');
    let loading = $state(false);
    let passwordInput: HTMLInputElement | null = $state(null);

    async function handleSubmit() {
        if (!password.trim()) return;

        loading = true;
        error = '';
        try {
            await login(password);
            await onClose();
        } catch (e: any) {
            // `login` answers every failure with the words to show here, so the copy has one home.
            error = e?.message || t('app.login.failed');
        } finally {
            loading = false;
        }
    }

    onMount(() => {
        // Queue after BaseModal's focus-trap microtask so the password field wins initial focus
        queueMicrotask(() => passwordInput?.focus());
    });
</script>

{#snippet footer()}
    <button type="button" class="login-btn btn btn-primary" disabled={loading || !password.trim()} onclick={handleSubmit}>
        {loading ? t('app.login.signingIn') : t('app.login.signIn')}
    </button>
{/snippet}

<BaseModal width="360px" modalClass="login-modal" overlayClass="login-modal-overlay" closeOnOverlayClick={false} footer={footer} onClose={() => {}} header={header}>
    {#if error}
        <div class="login-error" role="alert">{error}</div>
    {/if}

    <input
        id="login-password"
        bind:this={passwordInput}
        type="password"
        bind:value={password}
        placeholder={t('app.login.passwordAria')}
        autocomplete="current-password"
        aria-label={t('app.login.passwordAria')}
        onkeydown={(e) => { if (e.key === 'Enter') handleSubmit(); }}
    />
</BaseModal>

{#snippet header({ title: _title }: { title: string })}
    <div class="login-header">
        <h2>{t('app.login.signIn')}</h2>
    </div>
{/snippet}

<style>
    :global(.overlay.login-modal-overlay) {
        backdrop-filter: blur(4px);
    }
    :global(.modal.login-modal) {
        padding: 4px;
    }
    :global(.login-modal .modal-body) {
        display: flex;
        flex-direction: column;
        gap: 16px;
    }

    .login-header {
        text-align: center;
    }

    .login-header h2 {
        text-align: center;
    }

    .login-error {
        background: var(--color-status-red-bg);
        color: var(--color-text-primary);
        padding: 8px 12px;
        border-radius: var(--radius-sm);
        font-size: var(--text-base);
        text-align: center;
    }

    input {
        width: 100%;
        padding: 10px 14px;
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        background: var(--color-surface-2);
        color: var(--color-text-primary);
        font-size: var(--text-base);
        outline: none;
        transition: border-color var(--dur-base);
        box-sizing: border-box;
        font-family: inherit;
    }

    input:focus {
        border-color: var(--color-accent);
    }

    input::placeholder {
        color: var(--color-text-tertiary);
    }

    .login-btn {
        /* The footer asks for a full-width submit; the chip itself is `.btn .btn-primary`. */
        width: 100%;
        padding: 10px;
    }

</style>
