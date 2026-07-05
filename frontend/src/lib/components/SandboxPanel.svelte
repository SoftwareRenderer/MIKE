<script lang="ts">
    import { onMount } from 'svelte';
    import type { Task, Project } from '$lib/types';
    import { getProject } from '$lib/api';
    import shieldIcon from '$lib/assets/icons/shield.svg';

    let { task, onClose }: {
        task: Task;
        onClose: () => void;
    } = $props();

    let project = $state<Project | null>(null);
    let loading = $state(true);
    let error = $state('');

    onMount(async () => {
        try {
            project = await getProject(task.project_id);
        } catch (e) {
            error = String(e);
        } finally {
            loading = false;
        }
    });
</script>

<div class="sandbox-panel-overlay" onclick={(e) => { if (e.target === e.currentTarget) onClose(); }}>
    <div class="sandbox-panel" role="dialog" aria-modal="true" aria-label="Sandbox settings">
        <div class="panel-header">
            <div class="panel-title">
                <img src={shieldIcon} width="16" height="16" alt="" />
                <span>Sandboxed Task</span>
            </div>
            <button class="close-btn" onclick={onClose} aria-label="Close">✕</button>
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
                    <div class="setting-row">
                        <span class="setting-label">Forwarded Ports</span>
                        <span class="setting-value">{project.forward_ports.map(p => p.container_port).join(', ')}</span>
                    </div>
                    {/if}

                    <!-- Assigned ports (task-level) -->
                    {#if project.forward_ports && project.forward_ports.length > 0}
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
                </div>
            {/if}
        </div>
    </div>
</div>

<style>
    .sandbox-panel-overlay {
        position: fixed;
        inset: 0;
        background: rgba(0, 0, 0, 0.5);
        display: flex;
        align-items: center;
        justify-content: center;
        z-index: 1000;
        animation: fade-in 0.15s ease-out;
    }

    .sandbox-panel {
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-lg);
        padding: 20px 24px;
        max-width: 400px;
        width: 90%;
        max-height: 90vh;
        overflow-y: auto;
        animation: scale-in 0.15s ease-out;
    }

    .panel-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        margin-bottom: 16px;
    }

    .panel-title {
        display: flex;
        align-items: center;
        gap: 8px;
        font-size: 0.875rem;
        font-weight: 600;
        color: var(--color-text-primary);
    }

    .close-btn {
        width: 28px;
        height: 28px;
        display: flex;
        align-items: center;
        justify-content: center;
        border-radius: var(--radius-sm);
        background: transparent;
        color: var(--color-text-secondary);
        font-size: 1rem;
        cursor: pointer;
        border: none;
        transition: background 0.15s, color 0.15s;
    }

    .close-btn:hover {
        background: var(--color-surface-3);
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

    .setting-row:last-child {
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
        color: var(--color-accent);
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

    @keyframes fade-in {
        from { opacity: 0; }
        to { opacity: 1; }
    }

    @keyframes scale-in {
        from {
            opacity: 0;
            transform: scale(0.95);
        }
        to {
            opacity: 1;
            transform: scale(1);
        }
    }
</style>
