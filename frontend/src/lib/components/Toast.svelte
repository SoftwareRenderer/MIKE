<script lang="ts">
    import { dialogStore } from '$lib/stores';

    // The store owns the lifetime: `showToast` already schedules its own clearing after
    // TOAST_DURATION_MS, so a second timer here would only be able to disagree with it.
    let toast = $derived(dialogStore.toast);
</script>

{#if toast}
    <div class="toast" class:error={toast.type === 'error'} class:success={toast.type === 'success'} role="alert">
        {toast.message}
    </div>
{/if}

<style>
    /* Above every dialog: a fault the user has to see must not be hidden by the dialog that
       raised it. Nothing the app itself opens belongs above this. */
    .toast {
        position: fixed;
        bottom: 24px;
        left: 50%;
        transform: translateX(-50%);
        color: white;
        padding: 10px 20px;
        border-radius: var(--radius-md);
        font-size: var(--text-base);
        z-index: var(--z-toast);
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
