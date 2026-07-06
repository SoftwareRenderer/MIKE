<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import { slide } from 'svelte/transition';
    import type { Project, Task } from '$lib/types';
    import { updateProject, buildSandboxImageStream, getBoard, getProject } from '$lib/api';
    import { showConfirm } from '$lib/store';
    import type { PortForward, AssignedPort, SandboxMount } from '$lib/types';
    import shieldIcon from '$lib/assets/icons/shield.svg';
    import shieldOffIcon from '$lib/assets/icons/shield-off.svg';
    import ToggleSwitch from './ToggleSwitch.svelte';
    import DockerfilePicker from './DockerfilePicker.svelte';
    import DirectoryPicker from './DirectoryPicker.svelte';

    let { project, inProgressCount, onClose, onSaved }: {
        project: Project;
        inProgressCount: number;
        onClose: () => void;
        onSaved: () => void;
    } = $props();

    let sandboxEnabled = $state(project.sandbox_enabled ?? false);
    let networkIsolation = $state(project.sandbox_network_isolation ?? false);
    let sandboxMemoryLimit = $state(project.sandbox_memory_limit ?? 1024);
    let sandboxImage = $state(project.sandbox_image ?? '');
    let sandboxDockerfilePath = $state(project.sandbox_dockerfile_path ?? '');
    let forwardPorts = $state<PortForward[]>(project.forward_ports ?? []);
    let newContainerPort = $state('');
    let portError = $state('');
    let portsExpanded = $state(project.forward_ports && project.forward_ports.length > 0);
    let hasPendingPort = $derived(String(newContainerPort).trim() !== '');
    // Custom mounts
    let sandboxMounts = $state<SandboxMount[]>(project.sandbox_mounts ?? []);
    let newMountHostPath = $state('');
    let newMountContainerPath = $state('');
    let newMountMode = $state<'rw' | 'ro'>('rw');
    let mountError = $state('');
    let mountsExpanded = $state(project.sandbox_mounts && project.sandbox_mounts.length > 0);
    let hasPendingMount = $derived(newMountHostPath.trim() !== '' || newMountContainerPath.trim() !== '');
    let showDirectoryPicker = $state(false);
    // Save warning for un-added changes
    let saveWarning = $state('');
    // Active host ports from running tasks (container_port → host_port map)
    let activeHostPorts = $state<Map<number, number>>(new Map());
    let hasActivePorts = $derived(activeHostPorts.size > 0);
    // Active terminal ports from project-level terminal
    let terminalPorts = $state<AssignedPort[]>([]);
    let imageSource = $state<'image' | 'dockerfile'>(sandboxDockerfilePath ? 'dockerfile' : 'image');
    let dockerfileChanged = $derived(sandboxDockerfilePath !== (project.sandbox_dockerfile_path ?? ''));
    let showDockerfilePicker = $state(false);
    let imageTag = $derived(`sandbox-${project.name.replace(/ /g, '-').replace(/\//g, '-')}`);
    let saving = $state(false);
    let building = $state(false);
    let buildError = $state('');
    let buildLog = $state<string[]>([]);
    let buildLogEl: HTMLDivElement | null = null;
    let buildLogAtBottom = $state(true);
    let error = $state('');
    let isLocked = $derived(inProgressCount > 0);
    let modalEl: HTMLDivElement | null = null;

    function scrollBuildLog() {
        if (!buildLogEl || !buildLogAtBottom) return;
        requestAnimationFrame(() => {
            if (!buildLogEl) return;
            buildLogEl.scrollTop = buildLogEl.scrollHeight;
            requestAnimationFrame(() => {
                if (buildLogEl) buildLogEl.scrollTop = buildLogEl.scrollHeight;
            });
        });
    }

    function onBuildLogScroll() {
        if (!buildLogEl) return;
        const gap = buildLogEl.scrollHeight - buildLogEl.scrollTop - buildLogEl.clientHeight;
        buildLogAtBottom = gap < 50;
    }

    // Auto-enable network isolation when sandbox is toggled on
    let prevSandboxEnabled = $state(project.sandbox_enabled ?? false);
    let userToggledNetwork = $state(false);
    let networkIsolationBaseline = $state(project.sandbox_network_isolation ?? false);
    $effect(() => {
        if (sandboxEnabled && prevSandboxEnabled === false && !userToggledNetwork) {
            networkIsolation = true;
            networkIsolationBaseline = true;
        }
        prevSandboxEnabled = sandboxEnabled;
    });

    onMount(async () => {
        window.addEventListener('keydown', handleKeydown);
        // Fetch project to get active terminal ports
        try {
            const proj = await getProject(project.id);
            terminalPorts = proj.active_terminal_ports || [];
        } catch {
            // Ignore fetch errors
        }
        await refreshActivePorts();
    });

    onDestroy(() => {
        window.removeEventListener('keydown', handleKeydown);
    });

    async function refreshActivePorts() {
        try {
            const board = await getBoard(project.id);
            const map = new Map<number, number>();
            // Include project-level terminal ports
            for (const ap of terminalPorts) {
                map.set(ap.container_port, ap.host_port);
            }
            // Include task-level ports from running tasks
            for (const task of board.tasks) {
                if (task.execution_state === 'running' && task.assigned_ports) {
                    for (const ap of task.assigned_ports) {
                        map.set(ap.container_port, ap.host_port);
                    }
                }
            }
            activeHostPorts = map;
        } catch {
            // Ignore fetch errors
        }
    }

    function handleOverlayClick(e: MouseEvent) {
        if (e.target === e.currentTarget) {
            onClose();
        }
    }

    function handleKeydown(e: KeyboardEvent) {
        if (e.key === 'Escape') {
            e.preventDefault();
            onClose();
        }
    }

    function handleImageSourceChange(source: 'image' | 'dockerfile') {
        imageSource = source;
        buildError = '';
    }

    function handleBrowseDockerfile() {
        showDockerfilePicker = true;
    }

    function handleDockerfileSelected(path: string) {
        sandboxDockerfilePath = path;
        showDockerfilePicker = false;
    }

    function handleDockerfilePickerClose() {
        showDockerfilePicker = false;
    }

    // Port forwarding helpers
    function addPortForward() {
        portError = '';
        saveWarning = '';
        const container = parseInt(newContainerPort, 10);
        if (isNaN(container) || container < 1 || container > 65535) {
            portError = 'Port must be between 1 and 65535';
            return;
        }
        forwardPorts = [...forwardPorts, { container_port: container }];
        newContainerPort = '';
    }

    function removePortForward(index: number) {
        forwardPorts = forwardPorts.filter((_, i) => i !== index);
    }

    // Mount helpers
    function addMount() {
        mountError = '';
        saveWarning = '';
        if (!newMountHostPath.trim()) {
            mountError = 'Host path is required';
            return;
        }
        if (!newMountContainerPath.trim()) {
            mountError = 'Container path is required';
            return;
        }
        sandboxMounts = [...sandboxMounts, {
            host_path: newMountHostPath.trim(),
            container_path: newMountContainerPath.trim(),
            mode: newMountMode,
        }];
        newMountHostPath = '';
        newMountContainerPath = '';
        newMountMode = 'rw';
    }

    function removeMount(index: number) {
        sandboxMounts = sandboxMounts.filter((_, i) => i !== index);
    }

    function getDockerfileName(path: string): string {
        return path.split('/').pop() || path;
    }

    async function handleRebuild() {
        if (!sandboxDockerfilePath || building) return;
        building = true;
        buildError = '';
        buildLog = [];

        try {
            await buildSandboxImageStream(project.id, (line: string) => {
                if (line.startsWith('error: ')) {
                    buildError = line.slice(7);
                } else {
                    buildLog.push(line);
                    scrollBuildLog();
                }
            });
        } catch (e) {
            buildError = String(e);
        } finally {
            building = false;
        }
    }

    async function handleSave() {
        if (isLocked || saving) return;
        // Warn if user typed something in add fields but hasn't added them
        if (hasPendingPort || hasPendingMount) {
            saveWarning = 'You have un-added changes — click + to add them before saving.';
            return;
        }
        saving = true;
        error = '';
        saveWarning = '';

        const changes: {
            sandbox_enabled?: boolean;
            sandbox_network_isolation?: boolean;
            sandbox_image?: string;
            sandbox_dockerfile_path?: string;
            sandbox_memory_limit?: number;
            sandbox_forward_ports?: PortForward[];
            sandbox_mounts?: SandboxMount[];
        } = {};
        let needsConfirmation = false;

        if (sandboxEnabled !== (project.sandbox_enabled ?? false)) {
            changes.sandbox_enabled = sandboxEnabled;
            // Turning off sandbox requires confirmation
            if (!sandboxEnabled) {
                needsConfirmation = true;
            }
        }
        if (networkIsolation !== networkIsolationBaseline) {
            changes.sandbox_network_isolation = networkIsolation;
        }
        if (sandboxMemoryLimit !== (project.sandbox_memory_limit ?? 1024)) {
            changes.sandbox_memory_limit = sandboxMemoryLimit;
        }

        // Handle image source switch — clear unused field
        if (imageSource === 'image') {
            if (sandboxImage !== (project.sandbox_image ?? '')) {
                changes.sandbox_image = sandboxImage;
            }
            // Clear dockerfile path if switching away from it
            if (project.sandbox_dockerfile_path) {
                changes.sandbox_dockerfile_path = '';
            }
        } else {
            // dockerfile mode
            if (sandboxDockerfilePath !== (project.sandbox_dockerfile_path ?? '')) {
                changes.sandbox_dockerfile_path = sandboxDockerfilePath;
            }
            // Clear image if switching away from it
            if (project.sandbox_image) {
                changes.sandbox_image = '';
            }
        }

        // Port forwarding changes
        const originalPorts = project.forward_ports ?? [];
        const portsChanged = forwardPorts.length !== originalPorts.length ||
            forwardPorts.some((p, i) => p.container_port !== originalPorts[i]?.container_port);
        if (portsChanged) {
            changes.sandbox_forward_ports = forwardPorts;
        }

        // Custom mounts changes
        const originalMounts = project.sandbox_mounts ?? [];
        const mountsChanged = sandboxMounts.length !== originalMounts.length ||
            sandboxMounts.some((m, i) => m.host_path !== originalMounts[i]?.host_path ||
                m.container_path !== originalMounts[i]?.container_path ||
                m.mode !== originalMounts[i]?.mode);
        if (mountsChanged) {
            changes.sandbox_mounts = sandboxMounts;
        }

        // Validate: dockerfile mode requires a path
        if (imageSource === 'dockerfile' && !sandboxDockerfilePath) {
            error = 'Please select a Dockerfile before saving.';
            saving = false;
            return;
        }

        // No changes made
        if (Object.keys(changes).length === 0) {
            saving = false;
            onClose();
            return;
        }

        if (needsConfirmation) {
            const confirmed = await showConfirm({
                title: 'Disable Sandbox',
                message: `Disable sandbox isolation for "${project.name}"?\n\nTasks will run without gVisor sandboxing, executing commands directly on the host.`,
                confirmText: 'Disable',
                cancelText: 'Cancel',
            });
            if (!confirmed) {
                // Revert sandboxEnabled to original value
                sandboxEnabled = project.sandbox_enabled ?? false;
                saving = false;
                return;
            }
        }

        try {
            await updateProject(project.id, changes);
            onSaved();
        } catch (e) {
            error = String(e);
        } finally {
            saving = false;
        }

        // Rebuild image in background if dockerfile mode and path changed
        if (imageSource === 'dockerfile' && sandboxDockerfilePath && dockerfileChanged) {
            handleRebuild();
        }
    }
</script>

<div class="sandbox-config-overlay" role="dialog" aria-modal="true" aria-labelledby="sandbox-config-title" tabindex="-1" onclick={handleOverlayClick} onkeydown={(e) => { if (e.key === 'Escape') onClose(); }}>
    <div class="sandbox-config-dialog" bind:this={modalEl}>
        <div class="dialog-header">
            <h3 id="sandbox-config-title">Sandbox Configuration</h3>
            <button class="close-btn" onclick={onClose} aria-label="Close" title="Close">✕</button>
        </div>

        {#if isLocked}
            <div class="locked-notice">
                <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                    <rect x="3" y="7" width="10" height="7" rx="1.5" stroke="currentColor" stroke-width="1.3"/>
                    <path d="M5.5 7V5a2.5 2.5 0 015 0v2" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"/>
                </svg>
                <p>Cannot change sandbox settings while tasks are In Progress.</p>
            </div>
        {/if}

        <div class="config-options" class:locked={isLocked}>
            <!-- Sandbox enabled toggle -->
            <div class="config-row">
                <div class="config-info">
                    <div class="config-icon">
                        {#if sandboxEnabled}
                            <img src={shieldIcon} width="20" height="20" alt="" />
                        {:else}
                            <img src={shieldOffIcon} width="20" height="20" alt="" />
                        {/if}
                    </div>
                    <div class="config-text">
                        <span class="config-label">Sandbox Isolation</span>
                        <span class="config-desc">Use gVisor to isolate all AI actions within a sandbox</span>
                    </div>
                </div>
                <ToggleSwitch bind:checked={sandboxEnabled} disabled={isLocked} ariaLabel="Sandbox isolation" />
            </div>

            <!-- Options visible when sandbox is enabled -->
            {#if sandboxEnabled}
            <!-- Network isolation toggle -->
            <div class="config-row" onclickcapture={() => { userToggledNetwork = true; }}>
                <div class="config-info">
                    <div class="config-icon">
                        {#if networkIsolation}
                            <svg width="20" height="20" viewBox="0 0 20 20" fill="none" aria-hidden="true">
                                <path d="M10 1v10M5 11h10M7 15h6M8.5 19h3" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
                            </svg>
                        {:else}
                            <svg width="20" height="20" viewBox="0 0 20 20" fill="none" aria-hidden="true">
                                <circle cx="10" cy="17" r="1.2" fill="currentColor"/>
                                <path d="M8 13.5Q10 12 12 13.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" fill="none"/>
                                <path d="M4.5 10.5Q10 6 15.5 10.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" fill="none"/>
                                <path d="M1 7.5Q10 0.5 19 7.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" fill="none"/>
                            </svg>
                        {/if}
                    </div>
                    <div class="config-text">
                        <span class="config-label">Network Isolation</span>
                        <span class="config-desc">Block all network access</span>
                    </div>
                </div>
                <ToggleSwitch bind:checked={networkIsolation} disabled={isLocked} ariaLabel="Network isolation" />
            </div>

            <!-- Port forwarding subsection (nested under network isolation, only when isolation is off) -->
            {#if !networkIsolation}
            <div class="config-row config-row-subsection" class:expanded={portsExpanded} onclick={() => { portsExpanded = !portsExpanded; }}>
                <div class="config-info">
                    <div class="config-icon">
                        <svg width="20" height="20" viewBox="0 0 20 20" fill="none" aria-hidden="true">
                            <path d="M4 7H16M4 13H16" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
                            <path d="M12 4.5L16 7L12 9.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                            <path d="M8 10.5L4 13L8 15.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                        </svg>
                    </div>
                    <div class="config-text">
                        <span class="config-label" class:collapsed={!portsExpanded}>Forward Ports</span>
                        {#if portsExpanded}
                        <span class="config-desc">Host ports are auto-assigned when tasks run, and visible in the task's Sandbox Info panel.</span>
                        {/if}
                    </div>
                </div>
                <svg class="chevron" class:chevron-open={portsExpanded} width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                    <path d="M4 6L8 10L12 6" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                </svg>
            </div>

            {#if portsExpanded}
            <div class="ports-content" transition:slide={{ duration: 200 }}>
            <!-- Existing port forwards list -->
            {#if forwardPorts.length > 0}
            <div class="port-list">
                {#each forwardPorts as port, i}
                <div class="port-item">
                    <span class="port-mapping">
                        {#if activeHostPorts.has(port.container_port)}
                        <span class="port-host" title="Host port">{activeHostPorts.get(port.container_port)}</span>
                        <svg width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden="true">
                            <path d="M4 3L8 6L4 9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                        </svg>
                        {/if}
                        <span class="port-container" title="Container port">{port.container_port}</span>
                    </span>
                    <button type="button" class="btn-remove-port" onclick={() => removePortForward(i)} disabled={isLocked} title="Remove">
                        <svg width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden="true">
                            <path d="M3 3L9 9M9 3L3 9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"/>
                        </svg>
                    </button>
                </div>
                {/each}
            </div>
            {/if}

            <!-- No active host ports notice -->
            {#if forwardPorts.length > 0 && !hasActivePorts}
            <div class="port-no-active">Host ports assigned when tasks run</div>
            {/if}

            <!-- Add new port forward -->
            <div class="port-add-row">
                <input type="number" class="port-input" bind:value={newContainerPort} placeholder="Container port" min="1" max="65535" disabled={isLocked} aria-label="Container port" />
                <button type="button" class="btn-add-port" class:pending={hasPendingPort} onclick={addPortForward} disabled={isLocked} title="Add port forward">
                    <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
                        <path d="M7 2V12M2 7H12" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
                    </svg>
                </button>
            </div>

            {#if portError}
            <div class="port-error">{portError}</div>
            {/if}
            </div>
            {/if}
            {/if}

            <!-- Memory limit input -->
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
                        <span class="config-label">Memory Limit</span>
                        <span class="config-desc">Max container memory (MB)</span>
                    </div>
                </div>
                <div class="memory-input-group">
                    <input type="number" class="memory-input" bind:value={sandboxMemoryLimit} min="0" step="256" disabled={isLocked} aria-label="Maximum container memory in MB" />
                    <span class="memory-unit">MB</span>
                </div>
            </div>

            <!-- Custom mounts (collapsible) -->
            <div class="config-row config-row-expandable" class:expanded={mountsExpanded} onclick={() => { mountsExpanded = !mountsExpanded; }}>
                <div class="config-info">
                    <div class="config-icon">
                        <svg width="20" height="20" viewBox="0 0 20 20" fill="none" aria-hidden="true">
                            <path d="M2 4h5l1.5 1.5H18V16H2V4z" stroke="currentColor" stroke-width="1.3" fill="none"/>
                            <path d="M10 8v6M8 10l2-2 2 2" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                        </svg>
                    </div>
                    <div class="config-text">
                        <span class="config-label" class:collapsed={!mountsExpanded}>Custom Mounts</span>
                        {#if mountsExpanded}
                        <span class="config-desc">Bind mount host paths into sandbox containers.</span>
                        {/if}
                    </div>
                </div>
                <svg class="chevron" class:chevron-open={mountsExpanded} width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                    <path d="M4 6L8 10L12 6" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                </svg>
            </div>

            {#if mountsExpanded}
            <div class="mounts-content" transition:slide={{ duration: 200 }}>
            <!-- Existing mounts list -->
            {#if sandboxMounts.length > 0}
            <div class="mount-list">
                {#each sandboxMounts as mount, i}
                <div class="mount-item">
                    <div class="mount-info">
                        <span class="mount-path" title={mount.host_path}>{mount.host_path}</span>
                        <svg width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden="true">
                            <path d="M4 3L8 6L4 9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                        </svg>
                        <span class="mount-container" title={mount.container_path}>{mount.container_path}</span>
                        <span class="mount-mode" class:mode-ro={mount.mode === 'ro'}>{mount.mode}</span>
                    </div>
                    <button type="button" class="btn-remove-mount" onclick={() => removeMount(i)} disabled={isLocked} title="Remove">
                        <svg width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden="true">
                            <path d="M3 3L9 9M9 3L3 9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"/>
                        </svg>
                    </button>
                </div>
                {/each}
            </div>
            {/if}

            <!-- Add new mount -->
            <div class="mount-add-row">
                <input type="text" class="mount-input mount-input-host" bind:value={newMountHostPath} placeholder="/host/path" disabled={isLocked} aria-label="Host path" />
                <button type="button" class="btn-browse-mount" onclick={() => showDirectoryPicker = true} disabled={isLocked} title="Browse host directory">
                    <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
                        <path d="M2 4h4l1.5 1.5H12V12H2V4z" stroke="currentColor" stroke-width="1.2" fill="none"/>
                    </svg>
                </button>
                <svg width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden="true" class="mount-arrow">
                    <path d="M4 3L8 6L4 9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                </svg>
                <input type="text" class="mount-input" bind:value={newMountContainerPath} placeholder="/container/path" disabled={isLocked} aria-label="Container path" />
                <div class="radio-group radio-group-small">
                    <label class="radio-label" class:active={newMountMode === 'rw'}>
                        <input type="radio" name="mount-mode" value="rw" checked={newMountMode === 'rw'} onchange={() => newMountMode = 'rw'} disabled={isLocked} />
                        <span class="radio-text">rw</span>
                    </label>
                    <label class="radio-label" class:active={newMountMode === 'ro'}>
                        <input type="radio" name="mount-mode" value="ro" checked={newMountMode === 'ro'} onchange={() => newMountMode = 'ro'} disabled={isLocked} />
                        <span class="radio-text">ro</span>
                    </label>
                </div>
                <button type="button" class="btn-add-mount" class:pending={hasPendingMount} onclick={addMount} disabled={isLocked} title="Add mount">
                    <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
                        <path d="M7 2V12M2 7H12" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
                    </svg>
                </button>
            </div>

            {#if mountError}
            <div class="mount-error">{mountError}</div>
            {/if}
            </div>
            {/if}

            <!-- Image source selector -->
            <div class="config-row">
                <div class="config-info">
                    <div class="config-icon">
                        <svg width="20" height="20" viewBox="0 0 20 20" fill="none" aria-hidden="true">
                            <rect x="3" y="5" width="14" height="10" stroke="currentColor" stroke-width="1.3"/>
                            <path d="M6.5 5v10M10 5v10M13.5 5v10" stroke="currentColor" stroke-width="1"/>
                            <path d="M2 4.5h2.5v2H2zM2 13.5h2.5v2H2zM15.5 4.5h2.5v2h-2.5zM15.5 13.5h2.5v2h-2.5z" stroke="currentColor" stroke-width="1.1"/>
                        </svg>
                    </div>
                    <div class="config-text">
                        <span class="config-label">Image Source</span>
                        <span class="config-desc">Container image for sandbox</span>
                    </div>
                </div>
                <div class="radio-group">
                    <label class="radio-label" class:active={imageSource === 'image'}>
                        <input type="radio" name="image-source" value="image" checked={imageSource === 'image'} onchange={() => handleImageSourceChange('image')} disabled={isLocked} />
                        <span class="radio-text">Pre-built</span>
                    </label>
                    <label class="radio-label" class:active={imageSource === 'dockerfile'}>
                        <input type="radio" name="image-source" value="dockerfile" checked={imageSource === 'dockerfile'} onchange={() => handleImageSourceChange('dockerfile')} disabled={isLocked} />
                        <span class="radio-text">Dockerfile</span>
                    </label>
                </div>
            </div>

            <!-- Pre-built image input (only visible when image source is 'image') -->
            {#if imageSource === 'image'}
            <div class="config-row config-row-input">
                <div class="config-info">
                    <div class="config-text">
                        <span class="config-label">Image Name</span>
                        <span class="config-desc">Docker image for sandbox containers</span>
                    </div>
                </div>
                <input type="text" class="image-input" bind:value={sandboxImage} placeholder="ubuntu:latest" disabled={isLocked} aria-label="Sandbox image name" />
            </div>
            {:else}
            <!-- Dockerfile section -->
            <div class="dockerfile-section">
                <div class="dockerfile-header">
                    <div class="config-text">
                        <span class="config-label">Dockerfile</span>
                        <span class="config-desc">Select a Dockerfile on the server</span>
                    </div>
                    {#if sandboxDockerfilePath && !dockerfileChanged}
                    <button type="button" class="btn-rebuild" onclick={handleRebuild} disabled={isLocked || building} title="Rebuild image">
                        {#if building}
                            <svg class="spin" width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
                                <circle cx="7" cy="7" r="5" stroke="currentColor" stroke-width="1.5" stroke-dasharray="20 10" stroke-linecap="round"/>
                            </svg>
                        {:else}
                            <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
                                <path d="M1.5 7a5.5 5.5 0 0110.2-2.8M12.5 7a5.5 5.5 0 01-10.2 2.8" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" fill="none"/>
                                <path d="M11.5 1.5v3h-3M2.5 12.5v-3h3" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round" fill="none"/>
                            </svg>
                        {/if}
                        {building ? 'Building...' : 'Rebuild'}
                    </button>
                    {/if}
                </div>

                <div class="dockerfile-path-row">
                    {#if sandboxDockerfilePath}
                        <span class="dockerfile-path-display" title={sandboxDockerfilePath}>
                            <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
                                <path d="M2 2h4l1.5 1.5V12H2V2z" stroke="currentColor" stroke-width="1.2" fill="none"/>
                                <path d="M6.5 2H12V12H6.5V2z" stroke="currentColor" stroke-width="1.2" fill="none"/>
                            </svg>
                            <span class="dockerfile-path-text">{sandboxDockerfilePath}</span>
                        </span>
                    {:else}
                        <span class="dockerfile-path-placeholder">No Dockerfile selected</span>
                    {/if}
                    <button type="button" class="btn-browse" onclick={handleBrowseDockerfile} disabled={isLocked} title="Browse for Dockerfile">
                        <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
                            <path d="M2 4h10v7a1 1 0 01-1 1H3a1 1 0 01-1-1V4z" stroke="currentColor" stroke-width="1.2" fill="none"/>
                            <path d="M4 4V2.5A1.5 1.5 0 015.5 1h3A1.5 1.5 0 0110 2.5V4" stroke="currentColor" stroke-width="1.2" fill="none" stroke-linecap="round"/>
                        </svg>
                        Browse
                    </button>
                </div>

                {#if building}
                <div class="build-notice">Build running in background — you can close this dialog.</div>
                {/if}

                {#if building || buildLog.length > 0}
                <div class="build-log" bind:this={buildLogEl} role="log" aria-label="Build output" onscroll={onBuildLogScroll}>
                    {#each buildLog as line}
                        <div class="build-log-line">{line}</div>
                    {/each}
                    {#if buildLog.length === 0}
                        <div class="build-log-line build-log-pending">Building...</div>
                    {/if}
                </div>
                {/if}

                {#if buildError}
                <div class="build-error">{buildError}</div>
                {/if}

                <div class="image-tag-info">
                    <svg width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden="true">
                        <circle cx="6" cy="6" r="5" stroke="currentColor" stroke-width="1"/>
                        <path d="M6 3.5v0M6 5.5v3" stroke="currentColor" stroke-width="1.2" stroke-linecap="round"/>
                    </svg>
                    <span>Image tag: <code>{imageTag}</code></span>
                </div>
            </div>
            {/if}
            {/if}
        </div>

        {#if saveWarning}
            <div class="save-warning-message">{saveWarning}</div>
        {/if}
        {#if error}
            <div class="error-message">{error}</div>
        {/if}

        <div class="dialog-actions">
            <button type="button" class="btn-cancel" onclick={onClose} disabled={saving}>Cancel</button>
            <button type="button" class="btn-save" onclick={handleSave} disabled={saving || isLocked}>
                {saving ? 'Saving...' : 'Save'}
            </button>
        </div>
    </div>
</div>

{#if showDockerfilePicker}
    <DockerfilePicker initialPath={project.path} onClose={handleDockerfilePickerClose} onSelect={handleDockerfileSelected} />
{/if}

{#if showDirectoryPicker}
    <DirectoryPicker initialPath="/home" onClose={() => showDirectoryPicker = false} onSelect={(path: string) => { newMountHostPath = path; showDirectoryPicker = false; }} />
{/if}

<style>
    .sandbox-config-overlay {
        position: fixed;
        inset: 0;
        background: rgba(0, 0, 0, 0.6);
        display: flex;
        align-items: center;
        justify-content: center;
        z-index: 1000;
        animation: fade-in 0.15s ease-out;
    }

    .sandbox-config-dialog {
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-lg);
        padding: 20px 24px;
        max-width: 480px;
        width: 90%;
        max-height: 90vh;
        overflow-y: auto;
        animation: scale-in 0.15s ease-out;
    }

    .dialog-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        margin-bottom: 16px;
    }

    .dialog-header h3 {
        font-size: 0.875rem;
        font-weight: 600;
        margin: 0;
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
        transition: background 0.15s, color 0.15s;
    }

    .close-btn:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }

    .locked-notice {
        display: flex;
        align-items: center;
        gap: 10px;
        padding: 10px 14px;
        background: rgba(210, 153, 34, 0.1);
        border: 1px solid rgba(210, 153, 34, 0.3);
        border-radius: var(--radius-md);
        margin-bottom: 16px;
        color: var(--color-status-yellow, #d29922);
        font-size: 0.8125rem;
    }

    .locked-notice p {
        margin: 0;
        line-height: 1.4;
    }

    .config-options {
        display: flex;
        flex-direction: column;
        gap: 4px;
        padding: 12px;
        background: var(--color-surface-2);
        border-radius: var(--radius-md);
        margin-bottom: 16px;
        transition: opacity 0.15s;
    }

    .config-options.locked {
        opacity: 0.5;
        pointer-events: none;
    }

    .config-row {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 8px 0;
    }

    .config-row:not(:last-child) {
        border-bottom: 1px solid var(--color-border);
    }

    .config-row-input {
        gap: 12px;
    }

    .config-row-subsection {
        padding-left: 36px;
    }

    .config-row-subsection .config-icon {
        color: var(--color-text-secondary);
    }

    .config-row-subsection .config-label {
        font-weight: 400;
    }

    .config-row-subsection .config-label.collapsed {
        font-size: 0.75rem;
    }

    .config-row-subsection {
        cursor: pointer;
        user-select: none;
    }

    .config-row-subsection:hover,
    .config-row-subsection.expanded {
        background: var(--color-surface-3);
    }

    .config-row-subsection.expanded {
        border-bottom: none;
        border-radius: var(--radius-sm) var(--radius-sm) 0 0;
    }

    .config-row-subsection:not(.expanded):hover {
        border-radius: var(--radius-sm);
    }

    /* Expandable non-subsection rows (e.g. Custom Mounts) */
    .config-row-expandable:hover,
    .config-row-expandable.expanded {
        background: var(--color-surface-3);
        cursor: pointer;
        user-select: none;
    }

    .config-row-expandable.expanded {
        border-bottom: none;
        border-radius: var(--radius-sm) var(--radius-sm) 0 0;
    }

    .config-row-expandable:not(.expanded):hover {
        border-radius: var(--radius-sm);
    }

    .chevron {
        flex-shrink: 0;
        color: var(--color-text-secondary);
        transition: transform 0.15s ease;
    }

    .chevron-open {
        transform: rotate(180deg);
    }

    .image-input {
        flex-shrink: 0;
        width: 160px;
        padding: 4px 8px;
        border-radius: var(--radius-sm);
        border: 1px solid var(--color-border);
        background: var(--color-surface-1);
        color: var(--color-text-primary);
        font-size: 0.75rem;
        font-family: var(--font-mono, monospace);
        transition: border-color 0.15s;
    }

    .image-input:focus {
        outline: none;
        border-color: var(--color-accent);
    }

    .image-input:disabled {
        opacity: 0.5;
    }

    .memory-input-group {
        display: flex;
        align-items: center;
        gap: 4px;
        flex-shrink: 0;
    }

    .memory-input {
        width: 80px;
        padding: 4px 8px;
        border-radius: var(--radius-sm);
        border: 1px solid var(--color-border);
        background: var(--color-surface-1);
        color: var(--color-text-primary);
        font-size: 0.75rem;
        font-family: var(--font-mono, monospace);
        transition: border-color 0.15s;
        -moz-appearance: textfield;
        appearance: textfield;
    }

    .memory-input::-webkit-outer-spin-button,
    .memory-input::-webkit-inner-spin-button {
        -webkit-appearance: none;
        margin: 0;
    }

    .memory-input:focus {
        outline: none;
        border-color: var(--color-accent);
    }

    .memory-input:disabled {
        opacity: 0.5;
    }

    .memory-unit {
        font-size: 0.75rem;
        color: var(--color-text-secondary);
        white-space: nowrap;
    }

    .config-info {
        display: flex;
        align-items: center;
        gap: 12px;
        flex: 1;
        min-width: 0;
    }

    .config-icon {
        flex-shrink: 0;
        width: 24px;
        height: 24px;
        display: flex;
        align-items: center;
        justify-content: center;
        color: var(--color-primary);
    }

    .config-text {
        display: flex;
        flex-direction: column;
        min-width: 0;
    }

    .config-label {
        font-size: 0.8125rem;
        font-weight: 500;
        color: var(--color-text-primary);
    }

    .config-desc {
        font-size: 0.75rem;
        color: var(--color-text-secondary);
        margin-top: 1px;
    }

    /* Radio group */
    .radio-group {
        display: flex;
        gap: 0;
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        overflow: hidden;
        flex-shrink: 0;
    }

    .radio-label {
        display: flex;
        align-items: center;
        gap: 4px;
        padding: 3px 10px;
        font-size: 0.75rem;
        color: var(--color-text-secondary);
        cursor: pointer;
        transition: background 0.15s, color 0.15s;
        user-select: none;
    }

    .radio-label:first-child {
        border-right: 1px solid var(--color-border);
    }

    .radio-label.active {
        background: var(--color-accent);
        color: white;
    }

    .radio-label:hover:not(.active) {
        background: var(--color-surface-3);
    }

    .radio-label input[type="radio"] {
        display: none;
    }

    .radio-label:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }

    .radio-text {
        white-space: nowrap;
    }

    /* Dockerfile section */
    .dockerfile-section {
        padding: 8px 0 0;
        display: flex;
        flex-direction: column;
        gap: 8px;
    }

    .dockerfile-header {
        display: flex;
        align-items: flex-start;
        justify-content: space-between;
        gap: 8px;
    }

    .btn-rebuild {
        display: flex;
        align-items: center;
        gap: 4px;
        padding: 3px 8px;
        border-radius: var(--radius-sm);
        border: 1px solid var(--color-border);
        background: var(--color-surface-1);
        color: var(--color-text-primary);
        font-size: 0.75rem;
        cursor: pointer;
        transition: background 0.15s, border-color 0.15s;
        white-space: nowrap;
    }

    .btn-rebuild:hover {
        background: var(--color-surface-3);
        border-color: var(--color-accent);
    }

    .btn-rebuild:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }

    .dockerfile-path-row {
        display: flex;
        align-items: center;
        gap: 8px;
        width: 100%;
        box-sizing: border-box;
    }

    .dockerfile-path-display {
        flex: 1;
        display: flex;
        align-items: center;
        gap: 6px;
        padding: 6px 10px;
        border-radius: var(--radius-sm);
        border: 1px solid var(--color-border);
        background: var(--color-surface-1);
        color: var(--color-text-primary);
        font-size: 0.75rem;
        font-family: var(--font-mono, monospace);
        overflow: hidden;
        min-width: 0;
    }
    .dockerfile-path-text {
        flex: 1;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
        min-width: 0;
    }

    .dockerfile-path-placeholder {
        flex: 1;
        padding: 6px 10px;
        border-radius: var(--radius-sm);
        border: 1px dashed var(--color-border);
        background: var(--color-surface-1);
        color: var(--color-text-secondary);
        font-size: 0.75rem;
        font-style: italic;
    }

    .btn-browse {
        display: flex;
        align-items: center;
        gap: 4px;
        padding: 5px 10px;
        border-radius: var(--radius-sm);
        border: 1px solid var(--color-border);
        background: var(--color-surface-1);
        color: var(--color-text-primary);
        font-size: 0.75rem;
        cursor: pointer;
        transition: background 0.15s, border-color 0.15s;
        white-space: nowrap;
        flex-shrink: 0;
    }

    .btn-browse:hover:not(:disabled) {
        background: var(--color-surface-3);
        border-color: var(--color-accent);
    }

    .btn-browse:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }

    .image-tag-info {
        display: flex;
        align-items: center;
        gap: 6px;
        font-size: 0.75rem;
        color: var(--color-text-secondary);
    }

    .image-tag-info code {
        font-family: var(--font-mono, monospace);
        background: var(--color-surface-1);
        padding: 1px 4px;
        border-radius: 3px;
        font-size: 0.6875rem;
    }

    .build-error {
        padding: 6px 10px;
        background: rgba(248, 81, 73, 0.1);
        border: 1px solid rgba(248, 81, 73, 0.3);
        border-radius: var(--radius-sm);
        color: var(--color-status-red);
        font-size: 0.75rem;
    }

    .build-log {
        max-height: 180px;
        overflow-y: auto;
        padding: 8px 10px;
        background: var(--color-bg-elevated);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        font-family: var(--font-mono);
        font-size: 0.7rem;
        line-height: 1.5;
        color: var(--color-text-secondary);
        margin-bottom: 8px;
    }

    .build-log-line {
        white-space: pre-wrap;
        word-break: break-all;
    }

    .build-log-pending {
        color: var(--color-text-tertiary);
        font-style: italic;
    }

    .build-notice {
        padding: 6px 10px;
        margin-bottom: 6px;
        background: rgba(80, 140, 255, 0.08);
        border: 1px solid rgba(80, 140, 255, 0.2);
        border-radius: var(--radius-sm);
        color: var(--color-text-secondary);
        font-size: 0.75rem;
    }

    .save-warning-message {
        padding: 8px 12px;
        background: rgba(34, 197, 94, 0.08);
        border: 1px solid rgba(34, 197, 94, 0.3);
        border-radius: var(--radius-md);
        color: #22c55e;
        font-size: 0.8125rem;
        margin-bottom: 12px;
    }

    .error-message {
        padding: 8px 12px;
        background: rgba(248, 81, 73, 0.1);
        border: 1px solid rgba(248, 81, 73, 0.3);
        border-radius: var(--radius-md);
        color: var(--color-status-red);
        font-size: 0.8125rem;
        margin-bottom: 12px;
    }

    .dialog-actions {
        display: flex;
        gap: 8px;
        justify-content: flex-end;
    }

    .btn-cancel {
        padding: 6px 14px;
        border-radius: var(--radius-sm);
        border: 1px solid var(--color-border);
        background: var(--color-surface-2);
        color: var(--color-text-primary);
        font-size: 0.8125rem;
        cursor: pointer;
        transition: background 0.15s;
    }

    .btn-cancel:hover {
        background: var(--color-surface-3);
    }

    .btn-save {
        padding: 6px 14px;
        border-radius: var(--radius-sm);
        border: none;
        background: var(--color-accent);
        color: white;
        font-size: 0.8125rem;
        font-weight: 500;
        cursor: pointer;
        transition: background 0.15s;
    }

    .btn-save:hover {
        background: var(--color-accent-hover);
    }

    .btn-save:disabled,
    .btn-cancel:disabled {
        opacity: 0.5;
        cursor: not-allowed;
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

    @keyframes spin {
        from { transform: rotate(0deg); }
        to { transform: rotate(360deg); }
    }

    .spin {
        animation: spin 1s linear infinite;
    }

    /* Port forwarding subsection */
    .ports-content {
        background: var(--color-surface-3);
        padding: 0 0 4px;
        margin-top: -4px;
        border-radius: 0 0 var(--radius-sm) var(--radius-sm);
        border-bottom: 1px solid var(--color-border);
    }

    .port-list {
        display: flex;
        flex-direction: column;
        gap: 4px;
        padding-left: 36px;
        margin-top: 4px;
    }

    .port-item {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 4px 8px;
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
    }

    .port-mapping {
        display: flex;
        align-items: center;
        gap: 6px;
        font-size: 0.75rem;
        font-family: var(--font-mono, monospace);
        color: var(--color-text-primary);
    }

    .port-host {
        font-weight: 600;
        color: var(--color-accent);
    }

    .port-container {
        font-weight: 600;
        color: var(--color-accent);
    }

    .btn-remove-port {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 20px;
        height: 20px;
        border: none;
        background: transparent;
        color: var(--color-text-secondary);
        cursor: pointer;
        border-radius: var(--radius-sm);
        transition: background 0.15s, color 0.15s;
        flex-shrink: 0;
    }

    .btn-remove-port:hover:not(:disabled) {
        background: rgba(248, 81, 73, 0.15);
        color: var(--color-status-red);
    }

    .btn-remove-port:disabled {
        opacity: 0.4;
        cursor: not-allowed;
    }

    .port-add-row {
        display: flex;
        align-items: center;
        gap: 6px;
        padding-left: 36px;
    }

    .port-input {
        flex: 1;
        min-width: 120px;
        max-width: 200px;
        padding: 4px 8px;
        border-radius: var(--radius-sm);
        border: 1px solid var(--color-border);
        background: var(--color-surface-1);
        color: var(--color-text-primary);
        font-size: 0.75rem;
        font-family: var(--font-mono, monospace);
        transition: border-color 0.15s;
        -moz-appearance: textfield;
        appearance: textfield;
    }

    .port-input::-webkit-outer-spin-button,
    .port-input::-webkit-inner-spin-button {
        -webkit-appearance: none;
        margin: 0;
    }

    .port-input:focus {
        outline: none;
        border-color: var(--color-accent);
    }

    .port-input:disabled {
        opacity: 0.5;
    }

    .port-arrow {
        color: var(--color-text-secondary);
        flex-shrink: 0;
    }

    .btn-add-port {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 28px;
        height: 28px;
        border: 1px solid var(--color-border);
        background: var(--color-surface-1);
        color: var(--color-text-primary);
        cursor: pointer;
        border-radius: var(--radius-sm);
        transition: background 0.15s, border-color 0.15s;
        flex-shrink: 0;
    }

    .btn-add-port:hover:not(:disabled) {
        background: var(--color-surface-3);
        border-color: var(--color-accent);
    }

    .btn-add-port.pending {
        background: rgba(34, 197, 94, 0.15);
        border-color: rgba(34, 197, 94, 0.6);
        color: #22c55e;
    }

    .btn-add-port:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }

    .port-error {
        padding: 4px 8px;
        padding-left: 36px;
        background: rgba(248, 81, 73, 0.1);
        border: 1px solid rgba(248, 81, 73, 0.3);
        border-radius: var(--radius-sm);
        color: var(--color-status-red);
        font-size: 0.75rem;
    }

    .port-no-active {
        padding: 4px 8px;
        padding-left: 36px;
        color: var(--color-text-tertiary);
        font-size: 0.75rem;
        font-style: italic;
    }

    /* Mounts subsection */
    .mounts-content {
        background: var(--color-surface-3);
        padding: 0 0 4px;
        margin-top: -4px;
        border-radius: 0 0 var(--radius-sm) var(--radius-sm);
        border-bottom: 1px solid var(--color-border);
    }

    .mount-list {
        display: flex;
        flex-direction: column;
        gap: 4px;
        margin-top: 4px;
    }

    .mount-item {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 4px 8px;
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
    }

    .mount-info {
        display: flex;
        align-items: center;
        gap: 6px;
        font-size: 0.75rem;
        font-family: var(--font-mono, monospace);
        color: var(--color-text-primary);
        min-width: 0;
    }

    .mount-path {
        font-weight: 600;
        color: var(--color-text-primary);
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
        min-width: 0;
    }

    .mount-container {
        font-weight: 600;
        color: var(--color-accent);
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
        min-width: 0;
    }

    .mount-mode {
        font-size: 0.6875rem;
        padding: 1px 5px;
        border-radius: 3px;
        background: var(--color-surface-3);
        color: var(--color-text-secondary);
        font-weight: 600;
        flex-shrink: 0;
    }

    .mount-mode.mode-ro {
        background: rgba(210, 153, 34, 0.15);
        color: var(--color-status-yellow, #d29922);
    }

    .btn-remove-mount {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 20px;
        height: 20px;
        border: none;
        background: transparent;
        color: var(--color-text-secondary);
        cursor: pointer;
        border-radius: var(--radius-sm);
        transition: background 0.15s, color 0.15s;
        flex-shrink: 0;
    }

    .btn-remove-mount:hover:not(:disabled) {
        background: rgba(248, 81, 73, 0.15);
        color: var(--color-status-red);
    }

    .btn-remove-mount:disabled {
        opacity: 0.4;
        cursor: not-allowed;
    }

    .mount-add-row {
        display: flex;
        align-items: center;
        gap: 6px;
        margin: 4px 2px 0;
    }

    .mount-input {
        flex: 1;
        min-width: 100px;
        max-width: 180px;
        padding: 4px 8px;
        border-radius: var(--radius-sm);
        border: 1px solid var(--color-border);
        background: var(--color-surface-1);
        color: var(--color-text-primary);
        font-size: 0.75rem;
        font-family: var(--font-mono, monospace);
        transition: border-color 0.15s;
    }

    .mount-input-host {
        max-width: 160px;
    }

    .btn-browse-mount {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 28px;
        height: 28px;
        border: 1px solid var(--color-border);
        background: var(--color-surface-1);
        color: var(--color-text-primary);
        cursor: pointer;
        border-radius: var(--radius-sm);
        transition: background 0.15s, border-color 0.15s;
        flex-shrink: 0;
    }

    .btn-browse-mount:hover:not(:disabled) {
        background: var(--color-surface-3);
        border-color: var(--color-accent);
    }

    .btn-browse-mount:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }

    .mount-input:focus {
        outline: none;
        border-color: var(--color-accent);
    }

    .mount-input:disabled {
        opacity: 0.5;
    }

    .mount-arrow {
        color: var(--color-text-secondary);
        flex-shrink: 0;
    }

    .radio-group-small {
        border-radius: var(--radius-sm);
    }

    .radio-group-small .radio-label {
        padding: 2px 7px;
        font-size: 0.6875rem;
    }

    .btn-add-mount {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 28px;
        height: 28px;
        border: 1px solid var(--color-border);
        background: var(--color-surface-1);
        color: var(--color-text-primary);
        cursor: pointer;
        border-radius: var(--radius-sm);
        transition: background 0.15s, border-color 0.15s;
        flex-shrink: 0;
    }

    .btn-add-mount:hover:not(:disabled) {
        background: var(--color-surface-3);
        border-color: var(--color-accent);
    }

    .btn-add-mount.pending {
        background: rgba(34, 197, 94, 0.15);
        border-color: rgba(34, 197, 94, 0.6);
        color: #22c55e;
    }

    .btn-add-mount:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }

    .mount-error {
        padding: 4px 8px;
        background: rgba(248, 81, 73, 0.1);
        border: 1px solid rgba(248, 81, 73, 0.3);
        border-radius: var(--radius-sm);
        color: var(--color-status-red);
        font-size: 0.75rem;
    }

    @media (max-width: 768px) {
        .sandbox-config-dialog {
            width: 100vw;
            max-width: none;
            max-height: 100dvh;
            height: 100dvh;
            border-radius: 0;
            border: none;
            padding: 16px;
        }
        .mount-input::placeholder,
        .port-input::placeholder {
            font-size: 12px;
        }
    }
</style>
