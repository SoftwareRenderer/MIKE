<script lang="ts">
    import ToggleSwitch from '../ToggleSwitch.svelte';
    import { t } from '$lib/i18n';

    let {
        commitAssistedBy = $bindable(true),
        projectName = '',
        onDirty,
    }: {
        commitAssistedBy?: boolean;
        projectName?: string;
        onDirty?: () => void;
    } = $props();
</script>

<fieldset class="settings-section project-defaults">
    <h3 class="section-label">{t('settings.tab.projectDefaults')}</h3>
    <p class="section-description">{t('settings.project.description', { project: projectName })}</p>

    <div class="toggle-field">
        <div class="toggle-label-group">
            <span id="project-assisted-by-label" class="toggle-label">{t('settings.project.assistedBy')}</span>
            <span id="project-assisted-by-desc" class="toggle-desc">{t('settings.project.assistedByDesc')}</span>
        </div>
        <ToggleSwitch bind:checked={commitAssistedBy} ariaLabel={t('settings.project.assistedByAria')} ariaDescribedby="project-assisted-by-desc" onChange={() => onDirty?.()} />
    </div>
</fieldset>

<style>
    @import '$lib/styles/settings-section.css';
    .project-defaults {
        margin-bottom: 16px;
    }
    .section-label {
        font-size: 0.875rem;
        font-weight: 600;
        margin: 0 0 4px;
        color: var(--color-text-primary);
    }
    .section-description {
        font-size: 0.8125rem;
        color: var(--color-text-tertiary);
        margin: 0 0 12px;
    }
    .toggle-field {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 8px 0;
        gap: 12px;
    }
    .toggle-label-group {
        display: flex;
        flex-direction: column;
        gap: 2px;
        min-width: 0;
        flex: 1;
    }
    .toggle-label {
        font-size: 0.8125rem;
        font-weight: 500;
        color: var(--color-text-primary);
    }
    .toggle-desc {
        font-size: 0.75rem;
        color: var(--color-text-tertiary);
        line-height: 1.4;
    }
</style>
