<script lang="ts">

    import type { Task, Project, SandboxMemoryStats } from '$lib/types';
    import { getProject } from '$lib/api';
    import Icon from '$lib/components/Icon.svelte';
    import shieldIcon from '$lib/assets/icons/shield.svg?raw';
    import { isNearMemoryLimit, isMemoryLimitReached, formatSandboxMemory, formatMemoryLimitMB, memoryAlertSentences, projectMemoryLimitMB, taskSandboxMemoryFallback } from '$lib/utils/memory-stats';
    import { useMemoryAlert } from '$lib/composables/use-memory-alert.svelte';
    import { formatAssignedHost } from '$lib/utils/sandbox-ports';
import BaseModal from './BaseModal.svelte';
import SandboxSettingLists from './SandboxSettingLists.svelte';
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
    const limitMB = $derived(projectMemoryLimitMB(project));
    // Live usage while the container runs; afterwards, the peak recorded on the task row.
    const shownStats = $derived(stats ?? (project && limitMB !== null ? taskSandboxMemoryFallback(task, limitMB) : null));
    // The same alert the dot and the board card show; the OOM line below it stays once acknowledged.
    const memory = useMemoryAlert(() => task, () => limitMB, () => stats);
    const alert = $derived(memory.alert);

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
        {#snippet header()}
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
                    {#if alert}
                        <div class="memory-alert" role="alert">
                            <div class="memory-alert-title">{t('memory.alert.title')}</div>
                            {#each memoryAlertSentences(alert.alert) as line}
                                <div class="memory-alert-line">{line}</div>
                            {/each}
                            <div class="memory-alert-line">{t('memory.alert.action')}</div>
                            <button class="memory-alert-dismiss" onclick={() => { void memory.dismiss(); }}>{t('memory.alert.dismiss')}</button>
                        </div>
                    {:else if shownStats.oom_kills > 0}
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
                <div class="sandbox-settings">
                    <!-- Network isolation -->
                    <div class="sandbox-row">
                        <span class="sandbox-label">{t('sandbox.networkIsolation')}</span>
                        <span class="sandbox-value">{project.sandbox_network_isolation ? t('sandbox.enabled') : t('sandbox.disabled')}</span>
                    </div>

                    <!-- Memory limit -->
                    <div class="sandbox-row">
                        <span class="sandbox-label">{t('sandbox.memoryLimit')}</span>
                        <span class="sandbox-value">{formatMemoryLimitMB(limitMB ?? undefined)}</span>
                    </div>

                    <!-- Image -->
                    <div class="sandbox-row">
                        <span class="sandbox-label">{t('sandbox.image')}</span>
                        <span class="sandbox-value sandbox-value-mono">{project.sandbox_dockerfile_path || project.sandbox_image || t('sandbox.default')}</span>
                    </div>

                    <!-- Forward ports -->
                    {#if project.forward_ports && project.forward_ports.length > 0}
                    <div class="sandbox-row sandbox-row-flat">
                        <span class="sandbox-label">{t('sandbox.forwardedPorts')}</span>
                        <span class="sandbox-value">{project.forward_ports.map(p => p.container_port).join(', ')}</span>
                    </div>

                    <!-- Assigned ports (task-level) -->
                    <div class="sandbox-section sandbox-section-bounded">
                        <span class="sandbox-section-label">{t('sandbox.activePortMappings')}</span>
                        {#if task.assigned_ports && task.assigned_ports.length > 0}
                        <div class="sandbox-list">
                            {#each task.assigned_ports as port}
                            <div class="sandbox-item">
                                 <span class="sandbox-item-key" title={t('sandbox.hostAddress')}>{formatAssignedHost(port.host_port, port.host_ip)}</span>
                                <svg width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden="true">
                                    <path d="M4 3L8 6L4 9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                                </svg>
                                <span class="sandbox-item-text" title={t('sandbox.containerPort')}>{port.container_port}</span>
                            </div>
                            {/each}
                        </div>
                        {:else}
                        <div class="sandbox-note assigned-empty">{t('sandbox.portsIdle')}</div>
                        {/if}
                    </div>
                    {/if}

                    <SandboxSettingLists mounts={project.sandbox_mounts} envs={project.sandbox_envs} capabilities={project.sandbox_capabilities} />
                </div>
            {/if}
        </div>
</BaseModal>

<style>
    @import '$lib/styles/shared.css';
    @import '$lib/styles/sandbox-settings.css';

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

    .memory-alert {
        display: flex;
        flex-direction: column;
        gap: 4px;
        padding: 10px 12px;
        background: rgba(248, 81, 73, 0.1);
        border: 1px solid rgba(248, 81, 73, 0.3);
        border-radius: var(--radius-md);
    }

    .memory-alert-title {
        color: var(--color-status-red);
        font-size: 0.8125rem;
        font-weight: 600;
    }

    .memory-alert-line {
        color: var(--color-text-secondary);
        font-size: 0.75rem;
        line-height: 1.4;
    }

    .memory-alert-dismiss {
        align-self: flex-start;
        margin-top: 4px;
        padding: 4px 10px;
        font-size: 0.75rem;
        color: var(--color-text-primary);
        background: var(--color-surface-3);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        cursor: pointer;
    }

    .memory-alert-dismiss:hover {
        border-color: var(--color-text-tertiary);
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

    /* The list's inset; the muted italic line itself is .sandbox-note. */
    .assigned-empty {
        padding: 4px 8px;
    }

</style>
