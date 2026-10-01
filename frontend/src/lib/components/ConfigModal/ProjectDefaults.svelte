<script lang="ts">
    import ToggleSwitch from '../ToggleSwitch.svelte';
    import { t } from '$lib/i18n';
    import type { MCPServer } from '$lib/types';
    import { projectEnablesMCPServer } from '$lib/utils/mcp-project';

    let {
        commitAssistedBy = $bindable(true),
        mcpServerIDs = $bindable<string[]>([]),
        mcpOptions = [] as MCPServer[],
        projectName = '',
        onDirty,
    }: {
        commitAssistedBy?: boolean;
        /** This project's MCP opt-in list, written whole by the Save beside the trailer answer. */
        mcpServerIDs?: string[];
        /** The globally enabled servers — the only ones a project can be offered. */
        mcpOptions?: MCPServer[];
        projectName?: string;
        onDirty?: () => void;
    } = $props();

    function toggle(id: string, on: boolean) {
        mcpServerIDs = on ? [...mcpServerIDs.filter((s) => s !== id), id] : mcpServerIDs.filter((s) => s !== id);
        onDirty?.();
    }
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

    <div class="mcp-block">
        <span class="toggle-label">{t('settings.project.mcp')}</span>
        <span class="toggle-desc">{t('settings.project.mcpDesc')}</span>
        {#if mcpOptions.length === 0}
            <span class="toggle-desc">{t('settings.project.mcpNone')}</span>
        {:else}
            {#each mcpOptions as server (server.id)}
                <label class="mcp-row">
                    <input
                        type="checkbox"
                        checked={projectEnablesMCPServer({ mcp_server_ids: mcpServerIDs }, server.id)}
                        onchange={(e) => toggle(server.id || '', (e.target as HTMLInputElement).checked)}
                    />
                    <span class="toggle-label">{server.name}</span>
                    <span class="mcp-row-url">{server.url}</span>
                </label>
            {/each}
        {/if}
    </div>
</fieldset>

<style>
    @import '$lib/styles/settings-section.css';
    .project-defaults {
        margin-bottom: 16px;
    }
    .section-label {
        font-size: var(--text-base);
        font-weight: 600;
        margin: 0 0 4px;
        color: var(--color-text-primary);
    }
    .section-description {
        font-size: var(--text-sm);
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
        font-size: var(--text-sm);
        font-weight: 500;
        color: var(--color-text-primary);
    }
    .toggle-desc {
        font-size: var(--text-xs);
        color: var(--color-text-tertiary);
        line-height: 1.4;
    }
    .mcp-block {
        display: flex;
        flex-direction: column;
        gap: 6px;
        padding-top: 12px;
        border-top: 1px solid var(--color-border);
    }
    .mcp-row {
        display: flex;
        align-items: baseline;
        gap: 8px;
        cursor: pointer;
    }
    .mcp-row-url {
        font-family: var(--font-mono, monospace);
        font-size: var(--text-xs);
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }
</style>
