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
    <button type="button" class="login-btn" disabled={loading || !password.trim()} onclick={handleSubmit}>
        {loading ? t('app.login.signingIn') : t('app.login.signIn')}
    </button>
{/snippet}

<BaseModal title="" width="360px" zIndex={1000} closeOnOverlayClick={false} footer={footer} onClose={() => {}} header={header}>
    {#if error}
        <div class="login-error" role="alert">{error}</div>
    {/if}

    <input
        id="login-password"
        bind:this={passwordInput}
        type="password"
        bind:value={password}
        placeholder="Password"
        autocomplete="current-password"
        aria-label="Password"
        onkeydown={(e) => { if (e.key === 'Enter') handleSubmit(); }}
    />
</BaseModal>

{#snippet header({ title: _title }: { title: string })}
    <div class="login-header">
        <h2>{t('app.login.signIn')}</h2>
    </div>
{/snippet}

<style>
    :global(.overlay) {
        backdrop-filter: blur(4px);
    }
    :global(.modal) {
        padding: 4px;
        box-shadow: 0 8px 32px rgba(0, 0, 0, 0.4);
    }
    :global(.modal-header) {
        padding: 0 0 16px;
        border-bottom: none;
    }
    :global(.modal-body) {
        display: flex;
        flex-direction: column;
        gap: 16px;
        padding: 0;
    }
    :global(.modal-footer) {
        border-top: none;
        padding-top: 0;
        padding: 16px 0 0;
        justify-content: stretch;
    }

    .login-header {
        text-align: center;
    }

    .login-header h2 {
        text-align: center;
    }

    .login-error {
        background: rgba(248, 81, 73, 0.15);
        color: var(--color-text-primary);
        padding: 8px 12px;
        border-radius: var(--radius-sm);
        font-size: 0.875rem;
        text-align: center;
    }

    input {
        width: 100%;
        padding: 10px 14px;
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        background: var(--color-surface-2);
        color: var(--color-text-primary);
        font-size: 0.875rem;
        outline: none;
        transition: border-color 0.15s;
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
        width: 100%;
        padding: 10px;
        border: none;
        border-radius: var(--radius-md);
        background: var(--color-accent-dark);
        color: white;
        font-size: 0.875rem;
        font-weight: 500;
        cursor: pointer;
        transition: background 0.15s;
        font-family: inherit;
    }

    .login-btn:hover:not(:disabled) {
        background: var(--color-accent-hover);
    }

    .login-btn:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }
</style>
