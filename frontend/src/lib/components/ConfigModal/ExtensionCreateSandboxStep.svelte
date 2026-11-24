<script lang="ts">
    import { t } from '$lib/i18n';
    import ToggleSwitch from '../ToggleSwitch.svelte';
    import { DOCKER_DEFAULT_CAPABILITIES } from '../SandboxConfigModal/capabilities';

    /**
     * The create's two sandbox questions. The capability list is read from the Sandbox Configuration's
     * own, so one list exists.
     */
    let {
        allowNetwork = $bindable(false),
        defaultCapabilities = $bindable(false),
        disabled = false,
    } = $props();
</script>

<div class="sandbox-step">
    <p class="step-intro">{t('settings.extensions.createSandboxIntro')}</p>

    <div class="step-row">
        <div class="step-text">
            <span class="step-label">
                {t('settings.extensions.createSandboxNetworkLabel')}
                <span class="step-recommended">{t('settings.extensions.createSandboxRecommended')}</span>
            </span>
            <span class="step-desc">{t('settings.extensions.createSandboxNetworkDesc')}</span>
        </div>
        <ToggleSwitch
            bind:checked={allowNetwork}
            {disabled}
            ariaLabel={t('settings.extensions.createSandboxNetworkAria')}
        />
    </div>

    <div class="step-row">
        <div class="step-text">
            <span class="step-label">
                {t('settings.extensions.createSandboxCapsLabel')}
                <span class="step-recommended">{t('settings.extensions.createSandboxRecommended')}</span>
            </span>
            <span class="step-desc">{t('settings.extensions.createSandboxCapsDesc')}</span>
            <span class="step-caps">
                {#each DOCKER_DEFAULT_CAPABILITIES as cap}
                    <span class="step-cap">{cap}</span>
                {/each}
            </span>
        </div>
        <ToggleSwitch
            bind:checked={defaultCapabilities}
            {disabled}
            ariaLabel={t('settings.extensions.createSandboxCapsAria')}
        />
    </div>

    <p class="step-note">{t('settings.extensions.createSandboxNote')}</p>
</div>

<style>
    .sandbox-step {
        display: flex;
        flex-direction: column;
        gap: 0.75rem;
    }

    .step-intro,
    .step-note {
        font-size: var(--text-sm);
        color: var(--color-text-secondary);
        margin: 0;
    }

    .step-row {
        display: flex;
        align-items: flex-start;
        justify-content: space-between;
        gap: 0.75rem;
        padding: 0.5rem 0;
        border-top: 1px solid var(--color-border-subtle);
    }

    .step-text {
        display: flex;
        flex-direction: column;
        gap: 0.25rem;
    }

    .step-label {
        display: flex;
        align-items: center;
        gap: 0.4rem;
        font-size: var(--text-base);
        font-weight: 600;
        color: var(--color-text-primary);
    }

    .step-recommended {
        font-size: var(--text-xs);
        font-weight: 500;
        color: var(--color-accent);
    }

    .step-desc {
        font-size: var(--text-sm);
        color: var(--color-text-secondary);
    }

    .step-caps {
        display: flex;
        flex-wrap: wrap;
        gap: 0.25rem;
        margin-top: 0.15rem;
    }

    .step-cap {
        font-family: var(--font-mono);
        font-size: var(--text-xs);
        color: var(--color-text-tertiary);
        background: var(--color-surface-4);
        border-radius: var(--radius-sm);
        padding: 0.05rem 0.3rem;
    }
</style>
