<script lang="ts">
    import type { Extension, SandboxMount, SandboxEnv } from '$lib/types';
    import { t } from '$lib/i18n';
    import ToggleSwitch from '../ToggleSwitch.svelte';
    import MountModeRadios from './MountModeRadios.svelte';
    import {
        configNumberInput,
        configNumberValue,
        effectiveConfigFields,
        HOST_OWNED_LSP_CONFIG_KEY,
    } from '$lib/utils/extension-config';

    /**
     * The configuration one extension's manifest declared, as the Edit dialog edits it. The value of each
     * field is written straight into the dialog's draft, so a folder list or a variable list is exactly as
     * unsaved as the text inputs beside it.
     *
     * Fields the host manages itself are not here to begin with — `effectiveConfigFields` leaves them out,
     * because writing one back is a write the server refuses.
     */
    let { ext, config }: { ext: Extension; config: Record<string, any> } = $props();

    const fields = $derived(effectiveConfigFields(ext));
</script>

{#if fields.length > 0}
    <div class="config-fields">
        <h4>{t('settings.extensions.configuration')}</h4>
        {#each fields as field}
            <div class="config-field">
                <label class="field-label" for="edit-config-{field.key}">{field.title}</label>
                {#if field.description}
                    <p class="field-desc">{field.description}</p>
                {/if}
                {#if field.type === 'mount'}
                    {@const mounts = (config[field.key] ?? field.default ?? []) as SandboxMount[]}
                    <div class="mount-editor">
                        {#if mounts.length > 0}
                            <div class="mount-list">
                                <!-- A placeholder here is the path shape to type, not copy to translate; the
                                     accessible name is the aria-label. -->
                                {#each mounts as mount, i}
                                    <div class="mount-row">
                                        <input type="text" class="mount-input" value={mount.host_path} placeholder="/host/path" aria-label={t('sandbox.hostPath')} oninput={(e) => { const updated = [...mounts]; updated[i] = { ...mount, host_path: (e.target as HTMLInputElement).value }; config[field.key] = updated; }} />
                                        <svg width="12" height="12" viewBox="0 0 12 12" fill="none" class="mount-arrow"><path d="M4 3L8 6L4 9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/></svg>
                                        <input type="text" class="mount-input" value={mount.container_path} placeholder="/container/path" aria-label={t('sandbox.containerPath')} oninput={(e) => { const updated = [...mounts]; updated[i] = { ...mount, container_path: (e.target as HTMLInputElement).value }; config[field.key] = updated; }} />
                                        <MountModeRadios name={`mount-mode-${i}`} mode={mount.mode} onselect={(mode) => { const updated = [...mounts]; updated[i] = { ...mount, mode }; config[field.key] = updated; }} />
                                        <button type="button" class="btn-remove" onclick={() => { config[field.key] = mounts.filter((_, j) => j !== i); }} title={t('settings.extensions.remove')}><svg width="12" height="12" viewBox="0 0 12 12" fill="none"><path d="M3 3L9 9M9 3L3 9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"/></svg></button>
                                    </div>
                                {/each}
                            </div>
                        {/if}
                        <button type="button" class="btn-add" onclick={() => { config[field.key] = [...mounts, { host_path: '', container_path: '', mode: 'rw' as const }]; }} title={t('settings.extensions.addMount')}><svg width="14" height="14" viewBox="0 0 14 14" fill="none"><path d="M7 2V12M2 7H12" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg></button>
                    </div>
                {:else if field.type === 'env'}
                    {@const envs = (config[field.key] ?? field.default ?? []) as SandboxEnv[]}
                    <div class="env-editor">
                        {#if envs.length > 0}
                            <div class="env-list">
                                {#each envs as env, i}
                                    <div class="env-row">
                                        <input type="text" class="env-input env-input-key" value={env.key} placeholder={t('sandbox.envKeyPlaceholder')} oninput={(e) => { const updated = [...envs]; updated[i] = { ...env, key: (e.target as HTMLInputElement).value }; config[field.key] = updated; }} />
                                        <span class="env-equals">=</span>
                                        <input type="text" class="env-input" value={env.value ?? ''} placeholder={t('sandbox.envValueInputPlaceholder')} oninput={(e) => { const updated = [...envs]; updated[i] = { ...env, value: (e.target as HTMLInputElement).value }; config[field.key] = updated; }} />
                                        <button type="button" class="btn-remove" onclick={() => { config[field.key] = envs.filter((_, j) => j !== i); }} title={t('settings.extensions.remove')}><svg width="12" height="12" viewBox="0 0 12 12" fill="none"><path d="M3 3L9 9M9 3L3 9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"/></svg></button>
                                    </div>
                                {/each}
                            </div>
                        {/if}
                        <button type="button" class="btn-add" onclick={() => { config[field.key] = [...envs, { key: '', value: '' }]; }} title={t('settings.extensions.addVariable')}><svg width="14" height="14" viewBox="0 0 14 14" fill="none"><path d="M7 2V12M2 7H12" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg></button>
                    </div>
                {:else if field.type === 'number'}
                    <input
                        id="edit-config-{field.key}"
                        type="number"
                        value={configNumberValue(config, field)}
                        placeholder={field.key === HOST_OWNED_LSP_CONFIG_KEY ? t('settings.extensions.noLimit') : undefined}
                        oninput={(e) => config[field.key] = configNumberInput(field.key, (e.target as HTMLInputElement).value)}
                        class="config-input"
                    />
                {:else if field.type === 'boolean'}
                    <label class="toggle-label">
                        <ToggleSwitch
                            checked={config[field.key] ?? field.default ?? false}
                            onChange={() => config[field.key] = !(config[field.key] ?? field.default ?? false)}
                        />
                    </label>
                {:else}
                    <input
                        id="edit-config-{field.key}"
                        type={field.sensitive ? 'password' : 'text'}
                        value={config[field.key] ?? field.default ?? ''}
                        oninput={(e) => config[field.key] = (e.target as HTMLInputElement).value}
                        class="config-input"
                    />
                {/if}
            </div>
        {/each}
    </div>
{:else if ext.type !== 'lsp'}
    <p class="no-schema">{t('settings.extensions.noSchema')}</p>
{/if}

<style>
    .config-field {
        display: flex;
        flex-direction: column;
        gap: 0.25rem;
    }

    .config-input {
        background: var(--color-bg-input);
        border: 1px solid var(--color-border-subtle);
        border-radius: var(--radius-md);
        padding: 0.5rem 0.75rem;
        color: var(--color-text-primary);
        font-size: var(--text-base);
    }

    .config-input:focus {
        outline: none;
        border-color: var(--color-accent);
    }

    .mount-editor, .env-editor {
        display: flex;
        flex-direction: column;
        gap: 0.35rem;
    }

    .mount-list, .env-list {
        display: flex;
        flex-direction: column;
        gap: 0.35rem;
    }

    .mount-row, .env-row {
        display: flex;
        align-items: center;
        gap: 0.35rem;
    }

    .mount-input {
        background: var(--color-bg-input);
        border: 1px solid var(--color-border-subtle);
        border-radius: var(--radius-md);
        padding: 0.4rem 0.5rem;
        color: var(--color-text-primary);
        font-family: var(--font-mono, monospace);
        font-size: var(--text-xs);
        flex: 1;
        min-width: 0;
    }

    .mount-input:focus {
        outline: none;
        border-color: var(--color-accent);
    }

    .mount-arrow {
        flex-shrink: 0;
        color: var(--color-text-tertiary);
    }

    .env-input {
        background: var(--color-bg-input);
        border: 1px solid var(--color-border-subtle);
        border-radius: var(--radius-md);
        padding: 0.4rem 0.5rem;
        color: var(--color-text-primary);
        font-family: var(--font-mono, monospace);
        font-size: var(--text-xs);
        flex: 1;
        min-width: 0;
    }

    .env-input:focus {
        outline: none;
        border-color: var(--color-accent);
    }

    .env-input-key {
        max-width: 120px;
        flex: none;
    }

    .env-equals {
        color: var(--color-text-tertiary);
        font-family: var(--font-mono, monospace);
        font-size: var(--text-xs);
        flex-shrink: 0;
    }

    .btn-remove {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 22px;
        height: 22px;
        padding: 0;
        border: none;
        background: none;
        color: var(--color-text-tertiary);
        cursor: pointer;
        border-radius: var(--radius-sm);
        flex-shrink: 0;
    }

    .btn-remove:hover {
        opacity: 1;
        background: var(--color-surface-3);
        color: var(--color-status-red);
    }

    .btn-add {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 26px;
        height: 26px;
        padding: 0;
        border: 1px dashed var(--color-border-subtle);
        background: none;
        color: var(--color-text-secondary);
        cursor: pointer;
        border-radius: var(--radius-md);
        align-self: flex-start;
    }

    .btn-add:hover {
        border-color: var(--color-accent);
        color: var(--color-accent);
    }

    .no-schema {
        color: var(--color-text-tertiary);
        font-size: var(--text-base);
        margin-bottom: 1.5rem;
    }
</style>
