<script lang="ts">
    import { setCsrfToken } from '$lib/api';
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
            const res = await fetch('/api/auth/login', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                credentials: 'include',
                body: JSON.stringify({ password }),
            });
            if (!res.ok) {
                const body = await res.json().catch(() => ({}));
                error = body.error || 'Login failed';
                return;
            }
            // Extract CSRF token from response header
            const token = res.headers.get('X-CSRF-Token');
            if (token) setCsrfToken(token);
            await onClose();
        } catch (e) {
            error = 'Network error';
        } finally {
            loading = false;
        }
    }

    $effect(() => {
        setTimeout(() => passwordInput?.focus(), 50);
    });
</script>

{#snippet footer()}
    <button type="button" class="login-btn" disabled={loading || !password.trim()} onclick={handleSubmit}>
        {loading ? 'Signing in...' : 'Sign in'}
    </button>
{/snippet}

<BaseModal title="" width="360px" zIndex={1000} closeOnOverlayClick={false} footer={footer} onClose={() => {}} header={header}>
    {#if error}
        <div class="login-error" role="alert">{error}</div>
    {/if}

    <!-- svelte-ignore a11y_autofocus — intentional focus on login form -->
    <input
        id="login-password"
        bind:this={passwordInput}
        type="password"
        bind:value={password}
        placeholder="Password"
        autocomplete="current-password"
        autofocus
        aria-label="Password"
        onkeydown={(e) => { if (e.key === 'Enter') handleSubmit(); }}
    />
</BaseModal>

{#snippet header({ title: _title }: { title: string })}
    <div class="login-header">
        <h2>Sign in</h2>
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
