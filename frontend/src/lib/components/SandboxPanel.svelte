<script lang="ts">

    import type { Task, Project, SandboxMemoryStats } from '$lib/types';
    import { getProject } from '$lib/api';
    import Icon from '$lib/components/Icon.svelte';
    import shieldIcon from '$lib/assets/icons/shield.svg?raw';
    import { isNearMemoryLimit, isMemoryLimitReached, formatSandboxMemory, formatMemoryLimitMB, taskSandboxMemoryFallback } from '$lib/utils/memory-stats';
    import { formatAssignedHost } from '$lib/utils/sandbox-ports';
import BaseModal from './BaseModal.svelte';
import { t } from '$lib/i18n';

    let { task, stats, onClose }: {
        task: Task;
        stats: SandboxMemoryStats | null;
        onClose: () => void;
    } = $props();

    function memoryPct(s: SandboxMemoryStats): number {
        if (!s.running || s.memory_limit_mb <= 0 || s.memory_current_mb <= 0) return 0;
        return Math.min(100, Math.round((s.memory_current_mb / s.memory_limit_mb) * 100));
    }

    // Position of the peak tick on the bar, clamped to the track.
    function peakPct(s: SandboxMemoryStats): number {
        if (s.memory_limit_mb <= 0 || s.memory_peak_mb <= 0) return 0;
        return Math.min(100, Math.round((s.memory_peak_mb / s.memory_limit_mb) * 100));
    }

    let project = $state<Project | null>(null);
    let loading = $state(true);
    let error = $state('');

    // The limit the settings row states and the peak is drawn against are one number.
    const limitMB = $derived(project?.sandbox_memory_limit ?? 1024);
    // Live usage while the container runs; afterwards, the peak recorded on the task row.
    const shownStats = $derived(stats ?? (project ? taskSandboxMemoryFallback(task, limitMB) : null));

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

<BaseModal title={t('sandbox.infoTitle')} width="480px" onClose={onClose}>
        {#snippet header({ title })}
            <div class="panel-title">
                <Icon src={shieldIcon} width={16} height={16} />
                <span>{t('sandbox.sandboxedTask')}</span>
            </div>
        {/snippet}

        <div class="panel-body">
            <div class="status-notice">
{t('sandbox.isolationEnabled')}
            </div>

            {#if shownStats}
                <div class="memory-section">
                    <div class="memory-row">
                        <span class="memory-label">{t('sandbox.memoryUsage')}</span>
                        <span class="memory-value">{formatSandboxMemory(shownStats)}</span>
                    </div>
                    {#if shownStats.memory_limit_mb > 0}
                        <!-- Stays visible when not running: empty track + peak tick -->
                        <div class="memory-bar">
                            <div class="memory-bar-fill" class:warn={isNearMemoryLimit(shownStats)} style="width: {memoryPct(shownStats)}%"></div>
                            {#if shownStats.memory_peak_mb > 0}
                                <div class="memory-bar-peak" style="left: {peakPct(shownStats)}%" title={t('sandbox.peakMemory', { mb: shownStats.memory_peak_mb })}></div>
                            {/if}
                        </div>
                    {/if}
                    {#if shownStats.memory_peak_mb > 0}
                        <div class="memory-peak" class:reached={isMemoryLimitReached(shownStats)}>{t('sandbox.peakMemory', { mb: shownStats.memory_peak_mb })}</div>
                    {/if}
                    {#if shownStats.oom_kills > 0}
                        <div class="oom-warning" role="alert">
                            <span class="oom-warning-count">{shownStats.oom_kills}</span> {t('sandbox.oomKilled', { count: shownStats.oom_kills })}
                        </div>
                    {/if}
                </div>
            {/if}

            {#if loading}
                <div class="loading">{t('sandbox.loadingSettings')}</div>
            {:else if error}
                <div class="error">{error}</div>
            {:else if project}
                <div class="settings-list">
                    <!-- Network isolation -->
                    <div class="setting-row">
                        <span class="setting-label">{t('sandbox.networkIsolation')}</span>
                        <span class="setting-value">{project.sandbox_network_isolation ? t('sandbox.enabled') : t('sandbox.disabled')}</span>
                    </div>

                    <!-- Memory limit -->
                    <div class="setting-row">
                        <span class="setting-label">{t('sandbox.memoryLimit')}</span>
                        <span class="setting-value">{formatMemoryLimitMB(limitMB)}</span>
                    </div>

                    <!-- Image -->
                    <div class="setting-row">
                        <span class="setting-label">{t('sandbox.image')}</span>
                        <span class="setting-value setting-value-mono">{project.sandbox_dockerfile_path || project.sandbox_image || t('sandbox.default')}</span>
                    </div>

                    <!-- Forward ports -->
                    {#if project.forward_ports && project.forward_ports.length > 0}
                    <div class="setting-row setting-row-no-border">
                        <span class="setting-label">{t('sandbox.forwardedPorts')}</span>
                        <span class="setting-value">{project.forward_ports.map(p => p.container_port).join(', ')}</span>
                    </div>

                    <!-- Assigned ports (task-level) -->
                    <div class="assigned-section">
                        <span class="section-label">{t('sandbox.activePortMappings')}</span>
                        {#if task.assigned_ports && task.assigned_ports.length > 0}
                        <div class="assigned-list">
                            {#each task.assigned_ports as port}
                            <div class="assigned-item">
                                 <span class="assigned-host" title={t('sandbox.hostAddress')}>{formatAssignedHost(port.host_port, port.host_ip)}</span>
                                <svg width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden="true">
                                    <path d="M4 3L8 6L4 9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                                </svg>
                                <span class="assigned-container" title={t('sandbox.containerPort')}>{port.container_port}</span>
                            </div>
                            {/each}
                        </div>
                        {:else}
                        <div class="assigned-empty">{t('sandbox.portsIdle')}</div>
                        {/if}
                    </div>
                    {/if}

                    <!-- Custom mounts -->
                    {#if project.sandbox_mounts && project.sandbox_mounts.length > 0}
                    <div class="mounts-section">
                        <span class="section-label">{t('sandbox.customMounts')}</span>
                        <div class="mounts-list">
                            {#each project.sandbox_mounts as mount}
                            <div class="mount-item">
                                <span class="mount-host" title={mount.host_path}>{mount.host_path}</span>
                                <svg width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden="true">
                                    <path d="M4 3L8 6L4 9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                                </svg>
                                <span class="mount-container" title={mount.container_path}>{mount.container_path}</span>
                                <span class="mount-mode" class:mode-rw={mount.mode === 'rw'} class:mode-ro={mount.mode === 'ro'} title={mount.mode === 'rw' ? t('sandbox.readWrite') : t('sandbox.readOnly')}>{mount.mode}</span>
                            </div>
                            {/each}
                        </div>
                    </div>
                    {/if}

                    <!-- Environment variables -->
                    {#if project.sandbox_envs && project.sandbox_envs.length > 0}
                    <div class="envs-section">
                        <span class="section-label">{t('sandbox.environmentVariables')}</span>
                        <div class="envs-list">
                            {#each project.sandbox_envs as env}
                            <div class="env-item">
                                <span class="env-key" title={env.key}>{env.key}</span>
                                <span class="env-equals">=</span>
                                <span class="env-value" title={env.value}>{env.value || t('sandbox.empty')}</span>
                            </div>
                            {/each}
                        </div>
                    </div>
                    {/if}

                    <!-- Capabilities -->
                    {#if project.sandbox_capabilities && project.sandbox_capabilities.length > 0}
                    <div class="caps-section">
                        <span class="section-label">{t('sandbox.capabilities')}</span>
                        <div class="caps-list">
                            {#each project.sandbox_capabilities as cap}
                            <span class="caps-pill" title={cap}>{cap}</span>
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

    .memory-section {
        display: flex;
        flex-direction: column;
        gap: 6px;
        padding: 10px 12px;
        background: var(--color-surface-2);
        border-radius: var(--radius-md);
    }

    .memory-row {
        display: flex;
        justify-content: space-between;
        align-items: center;
        gap: 8px;
    }

    .memory-label {
        font-size: 0.8125rem;
        color: var(--color-text-secondary);
    }

    .memory-value {
        font-size: 0.8125rem;
        color: var(--color-text-primary);
        font-family: var(--font-mono, monospace);
    }

    .memory-bar {
        position: relative;
        height: 6px;
        border-radius: 3px;
        background: var(--color-surface-3);
    }

    .memory-bar-fill {
        height: 100%;
        border-radius: 3px;
        background: var(--color-accent);
        transition: width 0.3s ease;
    }

    .memory-bar-fill.warn {
        background: var(--color-status-red);
    }

    /* Red tick at the historical peak (drawn over the fill). */
    .memory-bar-peak {
        position: absolute;
        top: -3px;
        bottom: -3px;
        width: 2px;
        margin-left: -1px;
        border-radius: 1px;
        background: var(--color-status-red);
        transition: left 0.3s ease;
    }

    .memory-peak {
        font-size: 0.75rem;
        color: var(--color-text-tertiary);
    }

    .memory-peak.reached {
        color: var(--color-status-red);
    }

    .oom-warning {
        padding: 8px 10px;
        background: rgba(248, 81, 73, 0.1);
        border: 1px solid rgba(248, 81, 73, 0.3);
        border-radius: var(--radius-md);
        color: var(--color-status-red);
        font-size: 0.8125rem;
    }

    .oom-warning-count {
        font-weight: 700;
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
        overflow-wrap: anywhere;
    }

    .assigned-container {
        color: var(--color-text-primary);
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
        color: var(--color-text-primary);
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
        color: var(--color-status-red);
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
        color: var(--color-accent-light);
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }

    .env-equals {
        color: var(--color-text-secondary);
        flex-shrink: 0;
    }

    .env-value {
        color: var(--color-text-primary);
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }

    .caps-section {
        display: flex;
        flex-direction: column;
        gap: 6px;
    }

    .caps-list {
        display: flex;
        flex-wrap: wrap;
        gap: 4px;
    }

    .caps-pill {
        padding: 2px 8px;
        background: rgba(136, 192, 208, 0.12);
        border: 1px solid rgba(136, 192, 208, 0.25);
        border-radius: var(--radius-sm);
        font-size: 0.75rem;
        font-family: var(--font-mono, monospace);
        color: var(--color-accent-light, #88c0d0);
        max-width: 100%;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }

</style>
