<script lang="ts">
    
    import type { Task, Project } from '$lib/types';
    import { getProject } from '$lib/api';
    import shieldIcon from '$lib/assets/icons/shield.svg';
import BaseModal from './BaseModal.svelte';

    let { task, onClose }: {
        task: Task;
        onClose: () => void;
    } = $props();

    let project = $state<Project | null>(null);
    let loading = $state(true);
    let error = $state('');

    $effect(() => {
        async function load() {
            try {
                project = await getProject(task.project_id);
            } catch (e) {
                error = String(e);
            } finally {
                loading = false;
            }
        }
        load();
    });
</script>

<BaseModal width="480px" onClose={onClose}>
        <div slot="header" class="panel-title">
            <img src={shieldIcon} width="16" height="16" alt="" />
            <span>Sandboxed Task</span>
        </div>

        <div class="panel-body">
            <div class="status-notice">
Sandbox Isolation is enabled for this project
            </div>

            {#if loading}
                <div class="loading">Loading settings...</div>
            {:else if error}
                <div class="error">{error}</div>
            {:else if project}
                <div class="settings-list">
                    <!-- Network isolation -->
                    <div class="setting-row">
                        <span class="setting-label">Network Isolation</span>
                        <span class="setting-value">{project.sandbox_network_isolation ? 'Enabled' : 'Disabled'}</span>
                    </div>

                    <!-- Memory limit -->
                    <div class="setting-row">
                        <span class="setting-label">Memory Limit</span>
                        <span class="setting-value">{project.sandbox_memory_limit ?? 1024} MB</span>
                    </div>

                    <!-- Image -->
                    <div class="setting-row">
                        <span class="setting-label">Image</span>
                        <span class="setting-value setting-value-mono">{project.sandbox_dockerfile_path || project.sandbox_image || 'Default'}</span>
                    </div>

                    <!-- Forward ports -->
                    {#if project.forward_ports && project.forward_ports.length > 0}
                    <div class="setting-row setting-row-no-border">
                        <span class="setting-label">Forwarded Ports</span>
                        <span class="setting-value">{project.forward_ports.map(p => p.container_port).join(', ')}</span>
                    </div>

                    <!-- Assigned ports (task-level) -->
                    <div class="assigned-section">
                        <span class="section-label">Active Port Mappings</span>
                        {#if task.assigned_ports && task.assigned_ports.length > 0}
                        <div class="assigned-list">
                            {#each task.assigned_ports as port}
                            <div class="assigned-item">
                                <span class="assigned-host" title="Host port">{port.host_port}</span>
                                <svg width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden="true">
                                    <path d="M4 3L8 6L4 9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                                </svg>
                                <span class="assigned-container" title="Container port">{port.container_port}</span>
                            </div>
                            {/each}
                        </div>
                        {:else}
                        <div class="assigned-empty">Container has not started due to idle task or terminal</div>
                        {/if}
                    </div>
                    {/if}

                    <!-- Custom mounts -->
                    {#if project.sandbox_mounts && project.sandbox_mounts.length > 0}
                    <div class="mounts-section">
                        <span class="section-label">Custom Mounts</span>
                        <div class="mounts-list">
                            {#each project.sandbox_mounts as mount}
                            <div class="mount-item">
                                <span class="mount-host" title={mount.host_path}>{mount.host_path}</span>
                                <svg width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden="true">
                                    <path d="M4 3L8 6L4 9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                                </svg>
                                <span class="mount-container" title={mount.container_path}>{mount.container_path}</span>
                                <span class="mount-mode" class:mode-rw={mount.mode === 'rw'} class:mode-ro={mount.mode === 'ro'} title={mount.mode === 'rw' ? 'Read/Write' : 'Read-only'}>{mount.mode}</span>
                            </div>
                            {/each}
                        </div>
                    </div>
                    {/if}

                    <!-- Environment variables -->
                    {#if project.sandbox_envs && project.sandbox_envs.length > 0}
                    <div class="envs-section">
                        <span class="section-label">Environment Variables</span>
                        <div class="envs-list">
                            {#each project.sandbox_envs as env}
                            <div class="env-item">
                                <span class="env-key" title={env.key}>{env.key}</span>
                                <span class="env-equals">=</span>
                                <span class="env-value" title={env.value}>{env.value || '(empty)'}</span>
                            </div>
                            {/each}
                        </div>
                    </div>
                    {/if}
                </div>
            {/if}
        </div>
</BaseModal>

<style>
    @import '$lib/styles/shared.css';

    .panel-title {
        display: flex;
        align-items: center;
        gap: 8px;
        font-size: 0.875rem;
        font-weight: 600;
        color: var(--color-text-primary);
    }

    .panel-body {
        display: flex;
        flex-direction: column;
        gap: 12px;
    }

    .status-notice {
        padding: 10px 14px;
        background: rgba(80, 140, 255, 0.08);
        border: 1px solid rgba(80, 140, 255, 0.2);
        border-radius: var(--radius-md);
        color: var(--color-primary);
        font-size: 0.8125rem;
        line-height: 1.4;
    }

    .loading {
        color: var(--color-text-secondary);
        font-size: 0.8125rem;
        font-style: italic;
    }

    .error {
        padding: 8px 12px;
        background: rgba(248, 81, 73, 0.1);
        border: 1px solid rgba(248, 81, 73, 0.3);
        border-radius: var(--radius-md);
        color: var(--color-status-red);
        font-size: 0.8125rem;
    }

    .settings-list {
        display: flex;
        flex-direction: column;
        gap: 8px;
    }

    .setting-row {
        display: flex;
        justify-content: space-between;
        align-items: center;
        padding: 6px 0;
        border-bottom: 1px solid var(--color-border);
    }

    .setting-row:last-child,
    .setting-row-no-border {
        border-bottom: none;
    }

    .setting-label {
        font-size: 0.8125rem;
        color: var(--color-text-secondary);
    }

    .setting-value {
        font-size: 0.8125rem;
        color: var(--color-text-primary);
        text-align: right;
    }

    .setting-value-mono {
        font-family: var(--font-mono, monospace);
    }

    .assigned-section {
        margin-top: 4px;
        padding-top: 8px;
        border-top: 1px solid var(--color-border);
        display: flex;
        flex-direction: column;
        gap: 6px;
    }

    .section-label {
        font-size: 0.75rem;
        font-weight: 500;
        color: var(--color-text-secondary);
        text-transform: uppercase;
        letter-spacing: 0.3px;
    }

    .assigned-list {
        display: flex;
        flex-direction: column;
        gap: 4px;
    }

    .assigned-item {
        display: flex;
        align-items: center;
        gap: 6px;
        padding: 4px 8px;
        background: var(--color-surface-2);
        border-radius: var(--radius-sm);
        font-size: 0.75rem;
        font-family: var(--font-mono, monospace);
    }

    .assigned-host {
        font-weight: 600;
        color: var(--color-accent-light);
    }

    .assigned-container {
        color: var(--color-text-secondary);
    }

    .assigned-empty {
        padding: 4px 8px;
        color: var(--color-text-tertiary);
        font-size: 0.75rem;
        font-style: italic;
    }

    .mounts-section {
        display: flex;
        flex-direction: column;
        gap: 6px;
    }

    .mounts-list {
        display: flex;
        flex-direction: column;
        gap: 4px;
    }

    .mount-item {
        display: flex;
        align-items: center;
        gap: 6px;
        padding: 4px 8px;
        background: var(--color-surface-2);
        border-radius: var(--radius-sm);
        font-size: 0.75rem;
        font-family: var(--font-mono, monospace);
    }

    .mount-host {
        font-weight: 600;
        color: var(--color-accent-light);
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }

    .mount-container {
        color: var(--color-text-secondary);
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }

    .mount-mode {
        margin-left: auto;
        flex-shrink: 0;
        font-size: 0.6875rem;
        padding: 1px 5px;
        border-radius: 3px;
        background: var(--color-surface-3);
        color: var(--color-text-secondary);
        font-weight: 600;
    }

    .mount-mode.mode-rw {
        background: rgba(248, 81, 73, 0.15);
        color: #FF7575;
    }

    .mount-mode.mode-ro {
        background: rgba(210, 153, 34, 0.15);
        color: var(--color-status-yellow, #d29922);
    }

    .envs-section {
        display: flex;
        flex-direction: column;
        gap: 6px;
    }

    .envs-list {
        display: flex;
        flex-direction: column;
        gap: 4px;
    }

    .env-item {
        display: flex;
        align-items: center;
        gap: 4px;
        padding: 4px 8px;
        background: var(--color-surface-2);
        border-radius: var(--radius-sm);
        font-size: 0.75rem;
        font-family: var(--font-mono, monospace);
    }

    .env-key {
        font-weight: 600;
        color: var(--color-text-primary);
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }

    .env-equals {
        color: var(--color-text-secondary);
        flex-shrink: 0;
    }

    .env-value {
        color: var(--color-accent-light);
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }

</style>
