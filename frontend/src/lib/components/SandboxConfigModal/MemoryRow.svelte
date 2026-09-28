<script lang="ts">
    /**
     * The memory one task's sandbox may hold. It is the cap the container is created with, and the
     * number the memory budget admits a task against; a blank field is no limit at all, which is why
     * the field shows a placeholder instead of a 0 it would have to invent.
     */
    import { t } from '$lib/i18n';
    import type { SandboxDraftOwner } from './draft.svelte';

    let { draft, locked }: { draft: SandboxDraftOwner; locked: boolean } = $props();
</script>

<div class="config-row config-row-input">
    <div class="config-info">
        <div class="config-icon">
            <svg width="20" height="20" viewBox="0 0 20 20" fill="none" aria-hidden="true">
                <rect x="2" y="2" width="16" height="16" rx="1.5" stroke="currentColor" stroke-width="1.3"/>
                <rect x="5" y="5" width="10" height="10" rx="1" stroke="currentColor" stroke-width="1"/>
                <circle cx="4" cy="4" r="1" fill="currentColor"/>
                <path d="M4 2v-1M8 2v-1M12 2v-1M16 2v-1M4 18v1M8 18v1M12 18v1M16 18v1M2 4h-1M2 8h-1M2 12h-1M2 16h-1M18 4h1M18 8h1M18 12h1M18 16h1" stroke="currentColor" stroke-width="1.2" stroke-linecap="round"/>
            </svg>
        </div>
        <div class="config-text">
            <span class="config-label">{t('sandbox.memoryLimit')}</span>
            <span class="config-desc">{t('sandbox.memoryLimitDesc')}</span>
        </div>
    </div>
    <div class="memory-input-group">
        <input type="number" class="memory-input" bind:value={draft.memoryLimitMB} min="0" step="256" placeholder={t('sandbox.noLimit')} disabled={locked} aria-label={t('sandbox.memoryLimitAria')} />
        <span class="memory-unit">MB</span>
    </div>
</div>

<style>
    @import '$lib/styles/sandbox-config.css';

    .memory-input {
        width: 80px;
    }

    /* Blank field = unlimited; the placeholder says so instead of showing 0. */
    .memory-input::placeholder {
        color: var(--color-text-secondary);
        opacity: 1;
    }

    .memory-input-group {
        display: flex;
        align-items: center;
        gap: 4px;
        flex-shrink: 0;
    }

    .memory-unit {
        font-size: var(--text-xs);
        color: var(--color-text-secondary);
        white-space: nowrap;
    }
</style>
