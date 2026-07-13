<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import { toast } from '$lib/stores/dialogs';

    let visible = $state(false);
    let message = $state('');
    let type = $state<'error' | 'success'>('error');

    let unsub: (() => void) | null = null;
    let timeoutId: ReturnType<typeof setTimeout> | null = null;

    onMount(() => {
        unsub = toast.subscribe(value => {
            if (value) {
                message = value.message;
                type = value.type;
                visible = true;
                if (timeoutId) clearTimeout(timeoutId);
                timeoutId = setTimeout(() => {
                    visible = false;
                }, 4000);
            } else {
                visible = false;
            }
        });
    });

    onDestroy(() => {
        unsub?.();
        if (timeoutId) clearTimeout(timeoutId);
    });
</script>

{#if visible}
    <div class="toast" class:error={type === 'error'} class:success={type === 'success'} role="alert">
        {message}
    </div>
{/if}

<style>
    .toast {
        position: fixed;
        bottom: 24px;
        left: 50%;
        transform: translateX(-50%);
        color: white;
        padding: 10px 20px;
        border-radius: var(--radius-md);
        font-size: 0.875rem;
        z-index: 9999;
        box-shadow: 0 4px 12px rgba(0, 0, 0, 0.3);
        animation: slide-up 0.2s ease-out;
        max-width: 90vw;
        text-align: center;
    }
    .toast.error {
        background: var(--color-status-red-dark);
    }
    .toast.success {
        background: var(--color-status-green);
    }
    @keyframes slide-up {
        from {
            opacity: 0;
            transform: translateX(-50%) translateY(12px);
        }
        to {
            opacity: 1;
            transform: translateX(-50%) translateY(0);
        }
    }
</style>
