<script lang="ts">
    import { setCsrfToken } from '$lib/api';

    let { onClose }: { onClose: () => void | Promise<void> } = $props();
    let password = $state('');
    let error = $state('');
    let loading = $state(false);

    async function handleSubmit(e: Event) {
        e.preventDefault();
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
</script>

<div class="login-overlay" role="dialog" aria-modal="true" aria-label="Login">
    <div class="login-modal" onclick={(e) => e.stopPropagation()}>
        <form class="login-form" onsubmit={(e) => handleSubmit(e)}>
            <h2>Sign in</h2>
            <p class="login-subtitle">Enter your password to continue</p>

            {#if error}
                <div class="login-error" role="alert">{error}</div>
            {/if}

            <input
                type="password"
                bind:value={password}
                placeholder="Password"
                autocomplete="current-password"
                autofocus
                aria-label="Password"
            />

            <button type="submit" class="login-btn" disabled={loading || !password.trim()}>
                {loading ? 'Signing in...' : 'Sign in'}
            </button>
        </form>
    </div>
</div>

<style>
    .login-overlay {
        position: fixed;
        inset: 0;
        background: rgba(0, 0, 0, 0.6);
        display: flex;
        align-items: center;
        justify-content: center;
        z-index: 1000;
        backdrop-filter: blur(4px);
    }

    .login-modal {
        background: var(--color-surface-1, #1e1e2e);
        border: 1px solid var(--color-border, #313244);
        border-radius: var(--radius-lg, 12px);
        padding: 32px;
        width: 100%;
        max-width: 360px;
        box-shadow: 0 8px 32px rgba(0, 0, 0, 0.4);
    }

    .login-form {
        display: flex;
        flex-direction: column;
        gap: 16px;
    }

    .login-form h2 {
        margin: 0;
        font-size: 1.25rem;
        font-weight: 600;
        color: var(--color-text-primary, #cdd6f4);
        text-align: center;
    }

    .login-subtitle {
        margin: 0;
        font-size: 0.875rem;
        color: var(--color-text-secondary, #a6adc8);
        text-align: center;
    }

    .login-error {
        background: rgba(248, 81, 73, 0.15);
        color: var(--color-status-red, #f85149);
        padding: 8px 12px;
        border-radius: var(--radius-sm, 6px);
        font-size: 0.875rem;
        text-align: center;
    }

    input {
        width: 100%;
        padding: 10px 14px;
        border: 1px solid var(--color-border, #313244);
        border-radius: var(--radius-md, 8px);
        background: var(--color-surface-2, #181825);
        color: var(--color-text-primary, #cdd6f4);
        font-size: 0.875rem;
        outline: none;
        transition: border-color 0.15s;
        box-sizing: border-box;
    }

    input:focus {
        border-color: var(--color-accent, #89b4fa);
    }

    input::placeholder {
        color: var(--color-text-secondary, #a6adc8);
        opacity: 0.6;
    }

    .login-btn {
        width: 100%;
        padding: 10px;
        border: none;
        border-radius: var(--radius-md, 8px);
        background: var(--color-accent, #89b4fa);
        color: white;
        font-size: 0.875rem;
        font-weight: 500;
        cursor: pointer;
        transition: background 0.15s;
    }

    .login-btn:hover:not(:disabled) {
        background: var(--color-accent-hover, #74c7ec);
    }

    .login-btn:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }
</style>
