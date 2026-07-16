<script lang="ts">
    import { fade } from 'svelte/transition';
    import { SwUpdateDetector } from '$lib/composables/use-sw-update.svelte';

    let { detector: detectorProp }: { detector?: SwUpdateDetector } = $props();
    const detector = detectorProp ?? new SwUpdateDetector();
</script>

{#if detector.needsReload}
    <div
        class="sw-update-bar"
        transition:fade={{ duration: 150 }}
        role="alert"
        aria-live="polite"
    >
        <span class="sw-update-message">
            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                <path d="M13 11.5v-1a6 6 0 10-1.7 4.3" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"/>
                <polyline points="11.3,11.5 13.7,11.5 13.7,9.1" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
            </svg>
            New version available
        </span>
        <button
            type="button"
            class="bar-btn bar-btn-reload"
            onclick={detector.reload}
            aria-label="Reload page to load new version"
        >
            Reload
        </button>
    </div>
{/if}

<style>
    .sw-update-bar {
        position: fixed;
        bottom: 24px;
        left: 50%;
        transform: translateX(-50%);
        display: flex;
        align-items: center;
        gap: 12px;
        padding: 8px 12px;
        background: var(--color-surface-3);
        border: 1px solid var(--color-border-bright);
        border-radius: var(--radius-lg);
        box-shadow: 0 8px 32px rgba(0, 0, 0, 0.35);
        z-index: 100;
        user-select: none;
    }

    .sw-update-message {
        display: inline-flex;
        align-items: center;
        gap: 6px;
        font-size: 0.8125rem;
        font-weight: 500;
        color: var(--color-text-primary);
        white-space: nowrap;
    }

    .bar-btn-reload {
        display: inline-flex;
        align-items: center;
        gap: 6px;
        padding: 6px 12px;
        border: none;
        border-radius: var(--radius-sm);
        font-size: 0.8125rem;
        font-weight: 500;
        font-family: inherit;
        cursor: pointer;
        transition: background 0.15s ease, color 0.15s ease;
        white-space: nowrap;
        background: var(--color-accent-dark);
        color: white;
    }

    .bar-btn-reload:hover {
        background: var(--color-accent-hover);
    }

    @media (max-width: 768px) {
        .sw-update-bar {
            bottom: 16px;
            left: 8px;
            right: 8px;
            transform: none;
            padding: 10px 12px;
        }

        .bar-btn-reload {
            padding: 8px 14px;
            margin-left: auto;
        }
    }
</style>
