<script lang="ts">
    /**
     * Whether a turn may fetch a submodule. The fetch spends the operator's own credentials on an address a
     * repository's text chose, so it starts only where a person said it may, and never while the project is
     * isolated. Without either consent the task stops and the card asks
     * (`frontend/src/lib/components/Chat/SubmoduleNotice.svelte`).
     */
    import { t } from '$lib/i18n';
    import ToggleSwitch from '../ToggleSwitch.svelte';
    import type { SandboxDraftOwner } from './draft.svelte';

    let { draft, locked }: { draft: SandboxDraftOwner; locked: boolean } = $props();
</script>

<div class="config-row">
    <div class="config-info">
        <div class="config-icon">
            <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                <path d="M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z"/>
                <polyline points="3.27 6.96 12 12.01 20.73 6.96"/>
                <line x1="12" y1="22.08" x2="12" y2="12"/>
            </svg>
        </div>
        <div class="config-text">
            <span class="config-label">{t('sandbox.autoFetchSubmodules')}</span>
            <span class="config-desc">{draft.networkIsolation ? t('sandbox.autoFetchSubmodulesIsolated') : t('sandbox.autoFetchSubmodulesDesc')}</span>
        </div>
    </div>
    <ToggleSwitch bind:checked={draft.autoFetchSubmodules} disabled={locked} ariaLabel={t('sandbox.autoFetchSubmodulesAria')} />
</div>

<style>
    @import '$lib/styles/sandbox-config.css';
</style>
