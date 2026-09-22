<script lang="ts">
    import { onMount } from 'svelte';
    import { slide } from 'svelte/transition';
    import type { Project, Task } from '$lib/types';
    import { updateProject, buildSandboxImageStream, getBoard, getProject } from '$lib/api';
    import { DEFAULT_MEMORY_LIMIT_MB, formatMemoryLimitMB, memoryLimitToInput, parseMemoryLimitMB } from '$lib/utils/memory-stats';
    import { bindForInput, formatAssignedHost, formatForwardPort, formatForwardPorts, normalizeBind, portRowError } from '$lib/utils/sandbox-ports';
    import { appStore, dialogStore } from '$lib/stores';
    import { formatNetworkedExtensions, serverToolExtensions } from '$lib/utils/server-tool-network';
    import type { PortForward, AssignedPort, ProjectUpdates, SandboxEnv, SandboxMount } from '$lib/types';
    import Icon from '$lib/components/Icon.svelte';
    import shieldIcon from '$lib/assets/icons/shield.svg?raw';
    import shieldOffIcon from '$lib/assets/icons/shield-off.svg?raw';
    import BaseModal from './BaseModal.svelte';
import ToggleSwitch from './ToggleSwitch.svelte';
    import PathPicker from './PathPicker.svelte';
    import SandboxSettingLists from './SandboxSettingLists.svelte';
    import { t } from '$lib/i18n';
    import type { MessageKey } from '$lib/i18n';

    /** Files the Dockerfile picker lists and accepts. */
    function isDockerfile(name: string): boolean {
        const lower = name.toLowerCase();
        return lower === 'dockerfile' || lower.endsWith('.dockerfile');
    }

    let { project, inProgressCount: initialInProgressCount, onClose, onSaved }: {
        project: Project;
        inProgressCount: number;
        onClose: () => void;
        onSaved: () => void;
    } = $props();

    // Local count refreshed on mount to avoid stale projectCounts
    let inProgressCount = $state(0);
    $effect(() => {
        inProgressCount = initialInProgressCount;
    });

    let sandboxEnabled = $state(false);
    let networkIsolation = $state(false);
    let sandboxMemoryLimit = $state<number | null>(DEFAULT_MEMORY_LIMIT_MB);
    let sandboxImage = $state('');
    let sandboxDockerfilePath = $state('');
    let forwardPorts = $state<PortForward[]>([]);
    let editingPortIndex = $state<number | null>(null);
    let newContainerPort = $state('');
    let newHostIP = $state('');
    let portError = $state('');
    let portsExpanded = $state(false);
    let hasPendingPort = $derived(String(newContainerPort).trim() !== '');
    // Custom mounts
    let sandboxMounts = $state<SandboxMount[]>([]);
    let editingMountIndex = $state<number | null>(null);

    $effect(() => { sandboxEnabled = project.sandbox_enabled ?? false; });
    $effect(() => { networkIsolation = project.sandbox_network_isolation ?? false; });
    $effect(() => { sandboxMemoryLimit = memoryLimitToInput(project.sandbox_memory_limit ?? DEFAULT_MEMORY_LIMIT_MB); });
    $effect(() => { sandboxImage = project.sandbox_image ?? ''; });
    $effect(() => { sandboxDockerfilePath = project.sandbox_dockerfile_path ?? ''; });
    $effect(() => { forwardPorts = project.forward_ports ?? []; });
    $effect(() => { portsExpanded = !!project.forward_ports && project.forward_ports.length > 0; });
    $effect(() => { sandboxMounts = project.sandbox_mounts ?? []; });
    $effect(() => { mountsExpanded = !!project.sandbox_mounts && project.sandbox_mounts.length > 0; });
    $effect(() => { sandboxEnvs = project.sandbox_envs ?? []; });
    $effect(() => { envsExpanded = !!project.sandbox_envs && project.sandbox_envs.length > 0; });
    $effect(() => { networkIsolationBaseline = project.sandbox_network_isolation ?? false; });
    // See $lib/utils/server-tool-network for the rule.
    const serverToolNotice = $derived(
        serverToolExtensions(appStore.config, sandboxEnabled, networkIsolation)
    );
    let newMountHostPath = $state('');
    let newMountContainerPath = $state('');
    let newMountMode = $state<'rw' | 'ro'>('rw');
    let mountError = $state('');
    let mountsExpanded = $state(false);
    let hasPendingMount = $derived(newMountHostPath.trim() !== '' || newMountContainerPath.trim() !== '');
    let showDirectoryPicker = $state(false);
    // Environment variables
    let sandboxEnvs = $state<SandboxEnv[]>([]);
    let editingEnvIndex = $state<number | null>(null);
    let newEnvKey = $state('');
    let newEnvValue = $state('');
    let envError = $state('');
    let envsExpanded = $state(false);
    let hasPendingEnv = $derived(newEnvKey.trim() !== '');
    // Capabilities (multiselect/input combo)
    let sandboxCapabilities = $state<string[]>([]);
    let capsExpanded = $state(false);
    let capInputValue = $state('');
    let capDropdownOpen = $state(false);
    let capSelectedIndex = $state(-1);
    $effect(() => { sandboxCapabilities = project.sandbox_capabilities ?? []; });
    $effect(() => { capsExpanded = !!project.sandbox_capabilities && project.sandbox_capabilities.length > 0; });
    // Known capabilities for autocomplete suggestions
    const KNOWN_CAPABILITIES: string[] = [
        'CAP_CHOWN',
        'CAP_DAC_OVERRIDE',
        'CAP_DAC_READ_SEARCH',
        'CAP_FOWNER',
        'CAP_FSETID',
        'CAP_IPC_LOCK',
        'CAP_IPC_OWNER',
        'CAP_KILL',
        'CAP_LEASE',
        'CAP_LINUX_IMMUTABLE',
        'CAP_MKNOD',
        'CAP_NET_ADMIN',
        'CAP_NET_BIND_SERVICE',
        'CAP_NET_BROADCAST',
        'CAP_NET_RAW',
        'CAP_SETFCAP',
        'CAP_SETGID',
        'CAP_SETPCAP',
        'CAP_SETUID',
        'CAP_SYS_CHROOT',
        'CAP_SYS_PTRACE',
        'CAP_SYS_ADMIN',
        'CAP_SYS_BOOT',
        'CAP_SYS_MODULE',
        'CAP_SYS_NICE',
        'CAP_SYS_RAWIO',
        'CAP_SYS_RESOURCE',
        'CAP_SYS_TIME',
        'CAP_SYS_TTY_CONFIG',
        'CAP_BPF',
        'CAP_PERFMON',
        'CAP_AUDIT_CONTROL',
        'CAP_AUDIT_READ',
        'CAP_AUDIT_WRITE',
        'CAP_BLOCK_SUSPEND',
        'CAP_CHECKPOINT_RESTORE',
        'CAP_MAC_ADMIN',
        'CAP_MAC_OVERRIDE',
        'CAP_WAKE_ALARM',
        'CAP_SYSLOG',
    ];
    /** The one description per capability name — the NAME is the machine value and never translates. */
    function capDesc(value: string): string {
        return t(`sandbox.cap.${value}` as MessageKey);
    }

    // Filtered suggestions based on input
    let capSuggestions = $derived.by(() => {
        const query = capInputValue.trim().toUpperCase();
        if (!query) return [];
        return KNOWN_CAPABILITIES.filter(c =>
            !sandboxCapabilities.includes(c) &&
            c.toUpperCase().includes(query)
        ).slice(0, 8);
    });
    // Save warning for un-added changes
    let saveWarning = $state('');
    // Active host ports from running tasks (container_port → host_port map)
    let activeHostPorts = $state<Map<number, number>>(new Map());
    let hasActivePorts = $derived(activeHostPorts.size > 0);
    // Active terminal ports from project-level terminal
    let terminalPorts = $state<AssignedPort[]>([]);
    let imageSource = $state<'image' | 'dockerfile'>('image');
    $effect(() => { imageSource = sandboxDockerfilePath ? 'dockerfile' : 'image'; });
    let dockerfileChanged = $derived(sandboxDockerfilePath !== (project.sandbox_dockerfile_path ?? ''));
    let showDockerfilePicker = $state(false);
    // Mirrors sandbox.ProjectImageName: a project's Dockerfile image is a local build, so podman
    // stores it as `localhost/sandbox-<name>`. Keep the two in step.
    let imageTag = $derived(`localhost/sandbox-${project.name.toLowerCase().replace(/ /g, '-').replace(/\//g, '-')}`);
    let saving = $state(false);
    let building = $state(false);
    // Checked = builds started from this modal skip the layer cache. Not persisted.
    let buildNoCache = $state(false);
    let buildError = $state('');
    let buildLog = $state<string[]>([]);
    // svelte-ignore non_reactive_update — DOM ref via bind:this
    let buildLogEl: HTMLDivElement | null = null;
    let buildLogAtBottom = $state(true);
    let error = $state('');
    let isLocked = $derived(inProgressCount > 0);

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
    let prevSandboxEnabled = $state(false);
    let userToggledNetwork = $state(false);
    let networkIsolationBaseline = $state(false);
    let skipAutoEnable = $state(true);
    $effect(() => {
        if (skipAutoEnable) {
            skipAutoEnable = false;
            prevSandboxEnabled = sandboxEnabled;
            return;
        }
        if (sandboxEnabled && prevSandboxEnabled === false && !userToggledNetwork) {
            networkIsolation = true;
            networkIsolationBaseline = true;
        }
        prevSandboxEnabled = sandboxEnabled;
    });

    onMount(async () => {
        // Fetch project to get active terminal ports
        try {
            const proj = await getProject(project.id);
            terminalPorts = proj.active_terminal_ports || [];
        } catch {
            // Ignore fetch errors
        }
        await refreshActivePorts();
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
            // Refresh in-progress count from actual board data
            inProgressCount = board.tasks.filter((t: Task) => t.status === 'in_progress').length;
        } catch {
            // Ignore fetch errors
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
    function editPort(index: number) {
        editingPortIndex = index;
        newContainerPort = String(forwardPorts[index].container_port);
        newHostIP = bindForInput(forwardPorts[index].host_ip);
        portError = '';
    }

    function cancelPortEdit() {
        editingPortIndex = null;
        newContainerPort = '';
        newHostIP = '';
        portError = '';
    }

    function addPortForward() {
        portError = '';
        saveWarning = '';
        const message = portRowError(newContainerPort, newHostIP);
        if (message) {
            portError = message;
            return;
        }
        const container = Number(newContainerPort);
        const hostIP = normalizeBind(newHostIP) ?? '';
        const entry: PortForward = hostIP ? { container_port: container, host_ip: hostIP } : { container_port: container };
        if (editingPortIndex !== null) {
            const updated = [...forwardPorts];
            updated[editingPortIndex] = entry;
            forwardPorts = updated;
            editingPortIndex = null;
        } else {
            forwardPorts = [...forwardPorts, entry];
        }
        newContainerPort = '';
        newHostIP = '';
    }

    function removePortForward(index: number) {
        forwardPorts = forwardPorts.filter((_, i) => i !== index);
    }

    // Mount helpers
    function editMount(index: number) {
        editingMountIndex = index;
        newMountHostPath = sandboxMounts[index].host_path;
        newMountContainerPath = sandboxMounts[index].container_path;
        newMountMode = sandboxMounts[index].mode;
        mountError = '';
    }

    function cancelMountEdit() {
        editingMountIndex = null;
        newMountHostPath = '';
        newMountContainerPath = '';
        newMountMode = 'rw';
        mountError = '';
    }

    function addMount() {
        mountError = '';
        saveWarning = '';
        if (!newMountHostPath.trim()) {
            mountError = t('sandbox.mountHostPathError');
            return;
        }
        if (!newMountContainerPath.trim()) {
            mountError = t('sandbox.mountContainerPathError');
            return;
        }
        const mount = {
            host_path: newMountHostPath.trim(),
            container_path: newMountContainerPath.trim(),
            mode: newMountMode,
        };
        if (editingMountIndex !== null) {
            const updated = [...sandboxMounts];
            updated[editingMountIndex] = mount;
            sandboxMounts = updated;
            editingMountIndex = null;
        } else {
            sandboxMounts = [...sandboxMounts, mount];
        }
        newMountHostPath = '';
        newMountContainerPath = '';
        newMountMode = 'rw';
    }

    function removeMount(index: number) {
        sandboxMounts = sandboxMounts.filter((_, i) => i !== index);
    }

    // Env helpers
    function editEnv(index: number) {
        editingEnvIndex = index;
        newEnvKey = sandboxEnvs[index].key;
        newEnvValue = sandboxEnvs[index].value;
        envError = '';
    }

    function cancelEnvEdit() {
        editingEnvIndex = null;
        newEnvKey = '';
        newEnvValue = '';
        envError = '';
    }

    function addEnv() {
        envError = '';
        saveWarning = '';
        if (!newEnvKey.trim()) {
            envError = t('sandbox.envNameError');
            return;
        }
        // Check for duplicate keys
        const isDuplicate = sandboxEnvs.some((e, i) => 
            i !== editingEnvIndex && e.key === newEnvKey.trim()
        );
        if (isDuplicate) {
            envError = t('sandbox.envDuplicateError', { name: newEnvKey.trim() });
            return;
        }
        const env = {
            key: newEnvKey.trim(),
            value: newEnvValue,
        };
        if (editingEnvIndex !== null) {
            const updated = [...sandboxEnvs];
            updated[editingEnvIndex] = env;
            sandboxEnvs = updated;
            editingEnvIndex = null;
        } else {
            sandboxEnvs = [...sandboxEnvs, env];
        }
        newEnvKey = '';
        newEnvValue = '';
    }

    function removeEnv(index: number) {
        sandboxEnvs = sandboxEnvs.filter((_, i) => i !== index);
    }

    // Capabilities helpers
    function addCapability(cap: string) {
        const normalized = cap.trim().toUpperCase();
        if (!normalized || sandboxCapabilities.includes(normalized)) return;
        sandboxCapabilities = [...sandboxCapabilities, normalized];
        capInputValue = '';
        capDropdownOpen = false;
        capSelectedIndex = -1;
    }

    function removeCapability(cap: string) {
        sandboxCapabilities = sandboxCapabilities.filter(c => c !== cap);
    }

    function handleCapInputKeydown(e: KeyboardEvent) {
        if (e.key === 'Tab') {
            e.preventDefault();
            if (capSuggestions.length > 0) {
                const shift = e.shiftKey;
                if (capSelectedIndex < 0) {
                    capSelectedIndex = shift ? capSuggestions.length - 1 : 0;
                } else {
                    capSelectedIndex = shift
                        ? (capSelectedIndex - 1 + capSuggestions.length) % capSuggestions.length
                        : (capSelectedIndex + 1) % capSuggestions.length;
                }
                capInputValue = capSuggestions[capSelectedIndex];
            }
        } else if (e.key === 'Enter' && capInputValue.trim()) {
            e.preventDefault();
            // If a suggestion is selected, use it; otherwise add the raw input
            if (capSelectedIndex >= 0 && capSuggestions[capSelectedIndex]) {
                addCapability(capSuggestions[capSelectedIndex]);
            } else {
                addCapability(capInputValue);
            }
        } else if (e.key === 'ArrowDown' && capSuggestions.length > 0) {
            e.preventDefault();
            capSelectedIndex = capSelectedIndex < 0 ? 0 : (capSelectedIndex + 1) % capSuggestions.length;
            capInputValue = capSuggestions[capSelectedIndex];
        } else if (e.key === 'ArrowUp' && capSuggestions.length > 0) {
            e.preventDefault();
            capSelectedIndex = capSelectedIndex < 0 ? capSuggestions.length - 1 : (capSelectedIndex - 1 + capSuggestions.length) % capSuggestions.length;
            capInputValue = capSuggestions[capSelectedIndex];
        } else if (e.key === 'Backspace' && !capInputValue && sandboxCapabilities.length > 0) {
            // Remove last capability on backspace when input is empty
            removeCapability(sandboxCapabilities[sandboxCapabilities.length - 1]);
        } else if (e.key === 'Escape') {
            capDropdownOpen = false;
            capSelectedIndex = -1;
        }
    }

    function handleCapInputFocus() {
        capDropdownOpen = true;
    }

    function handleCapInputBlur() {
        // Delay to allow click on suggestion
        setTimeout(() => { capDropdownOpen = false; }, 150);
    }

    function handleCapInputClick() {
        capDropdownOpen = true;
    }

    function handleCapInputChange() {
        // Reset selection when user types
        capSelectedIndex = -1;
        capDropdownOpen = true;
    }

    async function handleRebuild() {
        if (!sandboxDockerfilePath || building) return;
        building = true;
        buildError = '';
        buildLog = [];

        try {
            if (dockerfileChanged) {
                await updateProject(project.id, { sandbox_dockerfile_path: sandboxDockerfilePath });
            }
            await buildSandboxImageStream(project.id, (line: string) => {
                if (line.startsWith('error: ')) {
                    buildError = line.slice(7);
                } else {
                    buildLog.push(line);
                    scrollBuildLog();
                }
            }, { noCache: buildNoCache });
        } catch (e) {
            buildError = String(e);
        } finally {
            building = false;
        }
    }

    async function handleSave() {
        if (isLocked || saving) return;
        // Warn if user typed something in add fields but hasn't added them
        if (hasPendingPort || hasPendingMount || hasPendingEnv) {
            saveWarning = t('sandbox.saveWarning');
            return;
        }
        saving = true;
        error = '';
        saveWarning = '';

        const changes: ProjectUpdates = {};
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
        // Blank (like 0 or a negative) means "no limit", sent as an explicit 0:
        // null/omitted tells the server to leave the limit unchanged, so an empty
        // field could never remove an existing limit.
        const memoryLimitMB = parseMemoryLimitMB(sandboxMemoryLimit);
        if (memoryLimitMB !== parseMemoryLimitMB(project.sandbox_memory_limit ?? DEFAULT_MEMORY_LIMIT_MB)) {
            changes.sandbox_memory_limit = memoryLimitMB;
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
            forwardPorts.some((p, i) => p.container_port !== originalPorts[i]?.container_port ||
                (p.host_ip ?? '') !== (originalPorts[i]?.host_ip ?? ''));
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

        // Environment variables changes
        const originalEnvs = project.sandbox_envs ?? [];
        const envsChanged = sandboxEnvs.length !== originalEnvs.length ||
            sandboxEnvs.some((e, i) => e.key !== originalEnvs[i]?.key ||
                e.value !== originalEnvs[i]?.value);
        if (envsChanged) {
            changes.sandbox_envs = sandboxEnvs;
        }

        // Capabilities changes
        const originalCaps = project.sandbox_capabilities ?? [];
        const capsChanged = sandboxCapabilities.length !== originalCaps.length ||
            sandboxCapabilities.some((c, i) => c !== originalCaps[i]);
        if (capsChanged) {
            changes.sandbox_capabilities = sandboxCapabilities;
        }

        // Validate: dockerfile mode requires a path
        if (imageSource === 'dockerfile' && !sandboxDockerfilePath) {
            error = t('sandbox.dockerfileRequired');
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
            const confirmed = await dialogStore.showConfirm({
                title: t('sandbox.disableTitle'),
                message: t('sandbox.disableConfirm', { name: project.name }),
                confirmText: t('sandbox.disableConfirmButton'),
                cancelText: t('sandbox.cancel'),
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

<BaseModal title={t('sandbox.title')} width={isLocked ? '480px' : '560px'} footer={isLocked ? undefined : footer} onClose={onClose}>
    {#if isLocked}
        <!-- Read-only view shown when tasks are in progress -->
        <div class="locked-view">
            <div class="locked-notice">
                <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                    <rect x="3" y="7" width="10" height="7" rx="1.5" stroke="currentColor" stroke-width="1.3"/>
                    <path d="M5.5 7V5a2.5 2.5 0 015 0v2" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"/>
                </svg>
                <p>{t('sandbox.lockedNotice')}</p>
            </div>

            <div class="sandbox-settings">
                <!-- Sandbox enabled -->
                <div class="sandbox-row">
                    <div class="setting-label-group">
                        <span class="sandbox-label">{t('sandbox.isolation')}</span>
                    </div>
                    <span class="sandbox-value">{sandboxEnabled ? t('sandbox.enabled') : t('sandbox.disabled')}</span>
                </div>

                {#if sandboxEnabled}
                <!-- Network isolation -->
                <div class="sandbox-row">
                    <span class="sandbox-label">{t('sandbox.networkIsolation')}</span>
                    <span class="sandbox-value">{networkIsolation ? t('sandbox.enabled') : t('sandbox.disabled')}</span>
                </div>

                <!-- Memory limit -->
                <div class="sandbox-row">
                    <span class="sandbox-label">{t('sandbox.memoryLimit')}</span>
                    <span class="sandbox-value">{formatMemoryLimitMB(sandboxMemoryLimit)}</span>
                </div>

                <!-- Image -->
                <div class="sandbox-row">
                    <span class="sandbox-label">{t('sandbox.image')}</span>
                    <span class="sandbox-value sandbox-value-mono">{sandboxDockerfilePath || sandboxImage || t('sandbox.default')}</span>
                </div>

                <!-- Forward ports -->
                {#if forwardPorts.length > 0}
                <div class="sandbox-row">
                    <span class="sandbox-label">{t('sandbox.forwardedPorts')}</span>
                    <span class="sandbox-value">{formatForwardPorts(forwardPorts)}</span>
                </div>

                <!-- Active port mappings -->
                {#if hasActivePorts}
                <div class="sandbox-section sandbox-section-bounded">
                    <span class="sandbox-section-label">{t('sandbox.activePortMappings')}</span>
                    <div class="sandbox-list">
                        {#each forwardPorts as port}
                        {#if activeHostPorts.has(port.container_port)}
                        <div class="sandbox-item">
                            <span class="sandbox-item-key" title={t('sandbox.hostPort')}>{formatAssignedHost(activeHostPorts.get(port.container_port) ?? 0, port.host_ip)}</span>
                            <svg width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden="true">
                                <path d="M4 3L8 6L4 9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                            </svg>
                            <span class="sandbox-item-text" title={t('sandbox.containerPort')}>{port.container_port}</span>
                        </div>
                        {/if}
                        {/each}
                    </div>
                </div>
                {/if}
                {/if}

                <SandboxSettingLists mounts={sandboxMounts} envs={sandboxEnvs} capabilities={sandboxCapabilities} bounded />
                {/if}
            </div>
        </div>
    {:else}
        <!-- Editable form (unchanged) -->
        <div class="config-options">
            <!-- Sandbox enabled toggle -->
            <div class="config-row">
                <div class="config-info">
                    <div class="config-icon">
                        {#if sandboxEnabled}
                            <Icon src={shieldIcon} width={20} height={20} />
                        {:else}
                            <Icon src={shieldOffIcon} color="var(--color-status-red)" width={20} height={20} />
                        {/if}
                    </div>
                    <div class="config-text">
                        <span class="config-label">{t('sandbox.isolation')}</span>
                        <span class="config-desc">{t('sandbox.isolationDesc')}</span>
                    </div>
                </div>
                <ToggleSwitch bind:checked={sandboxEnabled} disabled={isLocked} ariaLabel={t('sandbox.isolationAria')} />
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
                        <span class="config-label">{t('sandbox.networkIsolation')}</span>
                        <span class="config-desc">{t('sandbox.networkIsolationDesc')}</span>
                    </div>
                </div>
                <ToggleSwitch bind:checked={networkIsolation} disabled={isLocked} ariaLabel={t('sandbox.networkIsolationAria')} />
            </div>

            {#if serverToolNotice.length > 0}
                <div class="server-tool-notice">
                    {t('sandbox.networkIsolationServerTools', { extensions: formatNetworkedExtensions(serverToolNotice) })}
                </div>
            {/if}

            <!-- Port forwarding subsection (nested under network isolation, only when isolation is off) -->
            {#if !networkIsolation}
            <div class="config-row-group" class:expanded={portsExpanded}>
            <button type="button" class="config-row config-row-subsection" onclick={() => { portsExpanded = !portsExpanded; }} aria-expanded={portsExpanded} aria-label={t('sandbox.portsAria')}>
                <div class="config-info">
                    <div class="config-icon">
                        <svg width="20" height="20" viewBox="0 0 20 20" fill="none" aria-hidden="true">
                            <path d="M4 7H16M4 13H16" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
                            <path d="M12 4.5L16 7L12 9.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                            <path d="M8 10.5L4 13L8 15.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                        </svg>
                    </div>
                    <div class="config-text">
                        <span class="config-label" class:collapsed={!portsExpanded}>{t('sandbox.ports')}</span>
                        {#if portsExpanded}
                        <span class="config-desc">{t('sandbox.portsDesc')}</span>
                        {/if}
                    </div>
                </div>
                <svg class="chevron" class:chevron-open={portsExpanded} width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                    <path d="M4 6L8 10L12 6" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                </svg>
            </button>

            {#if portsExpanded}
            <div class="ports-content" transition:slide={{ duration: 200 }}>
            <!-- Existing port forwards list -->
            {#if forwardPorts.length > 0}
            <div class="port-list">
                {#each forwardPorts as port, i}
                <div class="port-item" class:editing={editingPortIndex === i} onclick={() => editPort(i)} role="button" tabindex="0" onkeydown={(e) => e.key === 'Enter' && editPort(i)}>
                    <span class="port-mapping">
                        {#if activeHostPorts.has(port.container_port)}
                        <span class="port-host" title={t('sandbox.hostPort')}>{activeHostPorts.get(port.container_port)}</span>
                        <svg width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden="true">
                            <path d="M4 3L8 6L4 9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                        </svg>
                        {/if}
                        <span class="port-container" title={t('sandbox.containerPort')}>{formatForwardPort(port)}</span>
                    </span>
                    <button type="button" class="btn-remove-port" onclick={(e) => { e.stopPropagation(); removePortForward(i); }} disabled={isLocked} title={t('sandbox.remove')}>
                        {@render closeIcon()}
                    </button>
                </div>
                {/each}
            </div>
            {/if}

            <!-- No active host ports notice -->
            {#if forwardPorts.length > 0 && !hasActivePorts}
            <div class="sandbox-note port-no-active">{t('sandbox.portsNoActive')}</div>
            {/if}

            <!-- Add new port forward -->
            <div class="port-add-row">
                {#if editingPortIndex !== null}
                    {@render cancelEdit(cancelPortEdit)}
                {/if}
                <input type="text" class="port-input port-input-bind" bind:value={newHostIP} placeholder={t('sandbox.hostInterface')} spellcheck="false" autocomplete="off" disabled={isLocked} aria-label={t('sandbox.hostInterfaceAria')} />
                <input type="number" class="port-input" bind:value={newContainerPort} placeholder={t('sandbox.containerPort')} min="1" max="65535" disabled={isLocked} aria-label={t('sandbox.containerPort')} />
                <button type="button" class="btn-add-port" class:pending={hasPendingPort} onclick={addPortForward} disabled={isLocked} title={editingPortIndex !== null ? t('sandbox.savePort') : t('sandbox.addPort')}>
                    {@render addIcon(editingPortIndex !== null)}
                </button>
            </div>

            {#if portError}
            <div class="port-error">{portError}</div>
            {:else}
            <div class="port-hint">{t('sandbox.hostInterfaceHint')}</div>
            {/if}
            </div>
            {/if}
            </div>
            {/if}

            <!-- Custom mounts (collapsible) -->
            <div class="config-row-group" class:expanded={mountsExpanded}>
            <button type="button" class="config-row config-row-expandable" onclick={() => { mountsExpanded = !mountsExpanded; }} aria-expanded={mountsExpanded} aria-label={t('sandbox.mountsAria')}>
                <div class="config-info">
                    <div class="config-icon">
                        <svg width="20" height="20" viewBox="0 0 20 20" fill="none" aria-hidden="true">
                            <path d="M2 4h5l1.5 1.5H18V16H2V4z" stroke="currentColor" stroke-width="1.3" fill="none"/>
                            <path d="M10 8v6M8 10l2-2 2 2" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                        </svg>
                    </div>
                    <div class="config-text">
                        <span class="config-label" class:collapsed={!mountsExpanded}>{t('sandbox.customMounts')}</span>
                        {#if mountsExpanded}
                        <span class="config-desc">{t('sandbox.mountsDesc')}</span>
                        {/if}
                    </div>
                </div>
                <svg class="chevron" class:chevron-open={mountsExpanded} width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                    <path d="M4 6L8 10L12 6" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                </svg>
            </button>

            {#if mountsExpanded}
            <div class="mounts-content" transition:slide={{ duration: 200 }}>
            <!-- Existing mounts list -->
            {#if sandboxMounts.length > 0}
            <div class="mount-list">
                {#each sandboxMounts as mount, i}
                <div class="mount-item" class:editing={editingMountIndex === i} onclick={() => editMount(i)} role="button" tabindex="0" onkeydown={(e) => e.key === 'Enter' && editMount(i)}>
                    <div class="mount-info">
                        <span class="mount-path" title={mount.host_path}>{mount.host_path}</span>
                        <svg width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden="true">
                            <path d="M4 3L8 6L4 9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                        </svg>
                        <span class="mount-container" title={mount.container_path}>{mount.container_path}</span>
                    </div>
                    <div class="mount-actions">
                        <span class="sandbox-mode" class:sandbox-mode-rw={mount.mode === 'rw'} class:sandbox-mode-ro={mount.mode === 'ro'} title={mount.mode === 'rw' ? t('sandbox.mountReadWrite') : t('sandbox.mountReadOnly')}>{mount.mode}</span>
                        <button type="button" class="btn-remove-mount" onclick={(e) => { e.stopPropagation(); removeMount(i); }} disabled={isLocked} title={t('sandbox.remove')}>
                        {@render closeIcon()}
                    </button>
                    </div>
                </div>
                {/each}
            </div>
            {/if}

            <!-- Add new mount -->
            <div class="mount-add-row">
                {#if editingMountIndex !== null}
                    {@render cancelEdit(cancelMountEdit)}
                {/if}
                <input type="text" class="mount-input mount-input-host" bind:value={newMountHostPath} placeholder="/host/path" disabled={isLocked} aria-label={t('sandbox.hostPath')} />
                <button type="button" class="btn-browse-mount" onclick={() => showDirectoryPicker = true} disabled={isLocked} title={t('sandbox.browseHostDirectory')}>
                    <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
                        <path d="M2 4h4l1.5 1.5H12V12H2V4z" stroke="currentColor" stroke-width="1.2" fill="none"/>
                    </svg>
                </button>
                <svg width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden="true" class="mount-arrow">
                    <path d="M4 3L8 6L4 9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                </svg>
                <input type="text" class="mount-input" bind:value={newMountContainerPath} placeholder="/container/path" disabled={isLocked} aria-label={t('sandbox.containerPath')} />
                <div class="radio-group radio-group-small">
                    <label class="radio-label" class:active={newMountMode === 'rw'} title={t('sandbox.mountReadWrite')}>
                        <input type="radio" name="mount-mode" value="rw" checked={newMountMode === 'rw'} onchange={() => newMountMode = 'rw'} disabled={isLocked} />
                        <span class="radio-text">rw</span>
                    </label>
                    <label class="radio-label" class:active={newMountMode === 'ro'} title={t('sandbox.mountReadOnly')}>
                        <input type="radio" name="mount-mode" value="ro" checked={newMountMode === 'ro'} onchange={() => newMountMode = 'ro'} disabled={isLocked} />
                        <span class="radio-text">ro</span>
                    </label>
                </div>
                <button type="button" class="btn-add-mount" class:pending={hasPendingMount} onclick={addMount} disabled={isLocked} title={editingMountIndex !== null ? t('sandbox.saveMount') : t('sandbox.addMount')}>
                    {@render addIcon(editingMountIndex !== null)}
                </button>
            </div>

            {#if mountError}
            <div class="mount-error">{mountError}</div>
            {/if}
            </div>
            {/if}
            </div>

            <!-- Environment variables (collapsible) -->
            <div class="config-row-group" class:expanded={envsExpanded}>
            <button type="button" class="config-row config-row-expandable" onclick={() => { envsExpanded = !envsExpanded; }} aria-expanded={envsExpanded} aria-label={t('sandbox.envsAria')}>
                <div class="config-info">
                    <div class="config-icon">
                        <svg width="20" height="20" viewBox="0 0 20 20" fill="none" aria-hidden="true">
                            <path d="M3 6h14M3 10h14M3 14h10" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"/>
                        </svg>
                    </div>
                    <div class="config-text">
                        <span class="config-label" class:collapsed={!envsExpanded}>{t('sandbox.environmentVariables')}</span>
                        {#if envsExpanded}
                        <span class="config-desc">{t('sandbox.envsDesc')}</span>
                        {/if}
                    </div>
                </div>
                <svg class="chevron" class:chevron-open={envsExpanded} width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                    <path d="M4 6L8 10L12 6" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                </svg>
            </button>

            {#if envsExpanded}
            <div class="envs-content" transition:slide={{ duration: 200 }}>
            <!-- Existing env vars list -->
            {#if sandboxEnvs.length > 0}
            <div class="env-list">
                {#each sandboxEnvs as env, i}
                <div class="env-item" class:editing={editingEnvIndex === i} onclick={() => editEnv(i)} role="button" tabindex="0" onkeydown={(e) => e.key === 'Enter' && editEnv(i)}>
                    <div class="env-info">
                        <span class="env-key" title={env.key}>{env.key}</span>
                        <span class="sandbox-eq">=</span>
                        <span class="env-value" title={env.value}>{env.value || t('sandbox.empty')}</span>
                    </div>
                    <button type="button" class="btn-remove-env" onclick={(e) => { e.stopPropagation(); removeEnv(i); }} disabled={isLocked} title={t('sandbox.remove')}>
                        {@render closeIcon()}
                    </button>
                </div>
                {/each}
            </div>
            {/if}

            <!-- Add new env var -->
            <div class="env-add-row">
                {#if editingEnvIndex !== null}
                    {@render cancelEdit(cancelEnvEdit)}
                {/if}
                <input type="text" class="env-input env-input-key" bind:value={newEnvKey} placeholder={t('sandbox.envKeyPlaceholder')} disabled={isLocked} aria-label={t('sandbox.envNameAria')} />
                <span class="sandbox-eq">=</span>
                <input type="text" class="env-input" bind:value={newEnvValue} placeholder={t('sandbox.envValueInputPlaceholder')} disabled={isLocked} aria-label={t('sandbox.envValueAria')} />
                <button type="button" class="btn-add-env" class:pending={hasPendingEnv} onclick={addEnv} disabled={isLocked} title={editingEnvIndex !== null ? t('sandbox.saveVariable') : t('sandbox.addVariable')}>
                    {@render addIcon(editingEnvIndex !== null)}
                </button>
            </div>

            {#if envError}
            <div class="env-error">{envError}</div>
            {/if}
            </div>
            {/if}
            </div>

            <!-- Capabilities (collapsible) -->
            <div class="config-row-group" class:expanded={capsExpanded}>
            <button type="button" class="config-row config-row-expandable" onclick={() => { capsExpanded = !capsExpanded; }} aria-expanded={capsExpanded} aria-label={t('sandbox.capsAria')}>
                <div class="config-info">
                    <div class="config-icon">
                        <svg width="20" height="20" viewBox="0 0 20 20" fill="none" aria-hidden="true">
                            <path d="M10 2l2.2 4.5 5 .7-3.6 3.5.9 5L10 13.3 5.5 15.7l.9-5L2.8 7.2l5-.7L10 2z" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round"/>
                        </svg>
                    </div>
                    <div class="config-text">
                        <span class="config-label" class:collapsed={!capsExpanded}>{t('sandbox.capabilities')}</span>
                        {#if capsExpanded}
                        <span class="config-desc">{t('sandbox.capsDesc')}</span>
                        {/if}
                    </div>
                </div>
                <svg class="chevron" class:chevron-open={capsExpanded} width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                    <path d="M4 6L8 10L12 6" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                </svg>
            </button>

            {#if capsExpanded}
            <div class="caps-content" transition:slide={{ duration: 200 }}>
            <!-- Multiselect input -->
            <div class="caps-input-wrapper">
                <!-- Selected tags -->
                {#if sandboxCapabilities.length > 0}
                <div class="caps-tags">
                    {#each sandboxCapabilities as cap}
                    <span class="caps-tag" title={cap}>
                        <span class="caps-tag-text">{cap}</span>
                        {#if !isLocked}
                        <button type="button" class="caps-tag-remove" onclick={() => removeCapability(cap)} title={t('sandbox.removeCapability', { cap })} aria-label={t('sandbox.removeCapability', { cap })}>
                            <svg width="10" height="10" viewBox="0 0 10 10" fill="none" aria-hidden="true">
                                <path d="M2 2L8 8M8 2L2 8" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"/>
                            </svg>
                        </button>
                        {/if}
                    </span>
                    {/each}
                </div>
                {/if}
                <!-- Input and dropdown -->
                <div class="caps-input-field" class:focus={capDropdownOpen}>
                    <input
                        type="text"
                        class="caps-input"
                        bind:value={capInputValue}
                        oninput={handleCapInputChange}
                        onkeydown={handleCapInputKeydown}
                        onfocus={handleCapInputFocus}
                        onblur={handleCapInputBlur}
                        onclick={handleCapInputClick}
                        placeholder={sandboxCapabilities.length === 0 ? t('sandbox.capSuggestionPlaceholder') : t('sandbox.capAddMorePlaceholder')}
                        disabled={isLocked}
                        autocomplete="off"
                        spellcheck="false"
                        aria-label={t('sandbox.addCapabilityAria')}
                        role="combobox"
                        aria-expanded={capDropdownOpen}
                        aria-controls="caps-dropdown-list"
                    />
                    <!-- Suggestions dropdown -->
                    {#if capDropdownOpen && capSuggestions.length > 0}
                    <div id="caps-dropdown-list" class="caps-dropdown" role="listbox">
                        {#each capSuggestions as suggestion, i}
                        <button type="button" class="caps-dropdown-item" class:selected={i === capSelectedIndex} role="option" aria-selected={i === capSelectedIndex} onclick={() => addCapability(suggestion)}>
                            <span class="caps-dropdown-name">{suggestion}</span>
                            <span class="caps-dropdown-desc">{capDesc(suggestion)}</span>
                        </button>
                        {/each}
                    </div>
                    {/if}
                </div>
            </div>
            </div>
            {/if}
            </div>

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
                        <span class="config-label">{t('sandbox.memoryLimit')}</span>
                        <span class="config-desc">{t('sandbox.memoryLimitDesc')}</span>
                    </div>
                </div>
                <div class="memory-input-group">
                    <input type="number" class="memory-input" bind:value={sandboxMemoryLimit} min="0" step="256" placeholder={t('sandbox.noLimit')} disabled={isLocked} aria-label={t('sandbox.memoryLimitAria')} />
                    <span class="memory-unit">MB</span>
                </div>
            </div>

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
                        <span class="config-label">{t('sandbox.imageSource')}</span>
                        <span class="config-desc">{t('sandbox.imageSourceDesc')}</span>
                    </div>
                </div>
                <div class="radio-group">
                    <label class="radio-label" class:active={imageSource === 'image'}>
                        <input type="radio" name="image-source" value="image" checked={imageSource === 'image'} onchange={() => handleImageSourceChange('image')} disabled={isLocked} />
                        <span class="radio-text">{t('sandbox.imagePrebuilt')}</span>
                    </label>
                    <label class="radio-label" class:active={imageSource === 'dockerfile'}>
                        <input type="radio" name="image-source" value="dockerfile" checked={imageSource === 'dockerfile'} onchange={() => handleImageSourceChange('dockerfile')} disabled={isLocked} />
                        <span class="radio-text">{t('sandbox.dockerfile')}</span>
                    </label>
                </div>
            </div>

            <!-- Pre-built image input (only visible when image source is 'image') -->
            {#if imageSource === 'image'}
            <div class="config-row config-row-input">
                <div class="config-info">
                    <div class="config-text">
                        <span class="config-label">{t('sandbox.imageName')}</span>
                        <span class="config-desc">{t('sandbox.imageNameDesc')}</span>
                    </div>
                </div>
                <input type="text" class="image-input" bind:value={sandboxImage} placeholder="docker.io/ubuntu:latest" disabled={isLocked} aria-label={t('sandbox.imageNameAria')} />
            </div>
            {:else}
            <!-- Dockerfile section -->
            <div class="dockerfile-section">
                <div class="dockerfile-header">
                    <div class="config-text">
                        <span class="config-label">{t('sandbox.dockerfile')}</span>
                        <span class="config-desc">{t('sandbox.dockerfileDesc')}</span>
                    </div>
                    {#if sandboxDockerfilePath}
                    <div class="build-actions">
                        <!-- Applies to the next build started here, not a saved setting -->
                        <label class="cache-toggle" class:disabled={isLocked || building}
                               title={t('sandbox.skipCacheTitle')}>
                            <input type="checkbox" aria-label={t('sandbox.disableCacheAria')} bind:checked={buildNoCache} disabled={isLocked || building} />
                            <span class="cache-toggle-text">{t('sandbox.disableCache')}</span>
                        </label>
                        {#if dockerfileChanged}
                        <button type="button" class="btn-build" onclick={handleRebuild} disabled={isLocked || building} title={t('sandbox.buildImage')}>
                            {#if building}
                                <svg class="spin" width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
                                    <circle cx="7" cy="7" r="5" stroke="currentColor" stroke-width="1.5" stroke-dasharray="20 10" stroke-linecap="round"/>
                                </svg>
                            {:else}
                                <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
                                    <path d="M7 2L2 12h10L7 2z" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round" fill="none"/>
                                </svg>
                            {/if}
                            {building ? t('sandbox.building') : t('sandbox.build')}
                        </button>
                        {:else}
                        <button type="button" class="btn-rebuild" onclick={handleRebuild} disabled={isLocked || building} title={buildNoCache ? t('sandbox.rebuildImageNoCache') : t('sandbox.rebuildImage')}>
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
                            {building ? t('sandbox.building') : t('sandbox.rebuild')}
                        </button>
                        {/if}
                    </div>
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
                        <span class="dockerfile-path-placeholder">{t('sandbox.noDockerfileSelected')}</span>
                    {/if}
                    <button type="button" class="btn-browse" onclick={handleBrowseDockerfile} disabled={isLocked} title={t('sandbox.browseDockerfile')}>
                        <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
                            <path d="M2 4h10v7a1 1 0 01-1 1H3a1 1 0 01-1-1V4z" stroke="currentColor" stroke-width="1.2" fill="none"/>
                            <path d="M4 4V2.5A1.5 1.5 0 015.5 1h3A1.5 1.5 0 0110 2.5V4" stroke="currentColor" stroke-width="1.2" fill="none" stroke-linecap="round"/>
                        </svg>
                        {t('sandbox.browse')}
                    </button>
                </div>

                {#if building}
                <div class="build-notice">{t('sandbox.buildNotice')}</div>
                {/if}

                {#if building || buildLog.length > 0}
                <div class="build-log" bind:this={buildLogEl} role="log" aria-label={t('sandbox.buildOutput')} onscroll={onBuildLogScroll}>
                    {#each buildLog as line}
                        <div class="build-log-line">{line}</div>
                    {/each}
                    {#if buildLog.length === 0}
                        <div class="build-log-line build-log-pending">{t('sandbox.building')}</div>
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
                    <span>{t('sandbox.imageTagLabel')} <code>{imageTag}</code></span>
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
    {/if}
</BaseModal>

{#snippet footer()}
    <div class="footer-wrapper">
        <button type="button" class="btn-secondary" onclick={onClose} disabled={saving}>{t('sandbox.cancel')}</button>
        <button type="button" class="btn-primary" onclick={handleSave} disabled={saving || isLocked}>
            {saving ? t('sandbox.saving') : t('sandbox.save')}
        </button>
    </div>
{/snippet}

{#snippet closeIcon()}
    <svg width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden="true">
        <path d="M3 3L9 9M9 3L3 9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"/>
    </svg>
{/snippet}

{#snippet addIcon(editing: boolean)}
    {#if editing}
        <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
            <path d="M3 8L6 11L11 6" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
        </svg>
    {:else}
        <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
            <path d="M7 2V12M2 7H12" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
        </svg>
    {/if}
{/snippet}

{#snippet cancelEdit(oncancel: () => void)}
    <button type="button" class="btn-cancel-edit" onclick={oncancel} title={t('sandbox.cancelEdit')}>
        <svg width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden="true">
            <path d="M2 2L10 10M10 2L2 10" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"/>
        </svg>
    </button>
{/snippet}

{#if showDockerfilePicker}
    <PathPicker title={t('sandbox.selectDockerfile')} initialPath={project.path} emptyLabel={t('sandbox.noDockerfilesFound')} selectTitle={t('sandbox.selectDockerfileTitle')} fileFilter={isDockerfile} onClose={handleDockerfilePickerClose} onSelect={handleDockerfileSelected} />
{/if}

{#if showDirectoryPicker}
    <PathPicker title={t('sandbox.selectDirectory')} initialPath="/home" emptyLabel={t('sandbox.noSubdirectories')} selectTitle={t('sandbox.selectDirectoryTitle')} onClose={() => showDirectoryPicker = false} onSelect={(path: string) => { newMountHostPath = path; showDirectoryPicker = false; }} />
{/if}

<style>
    @import '$lib/styles/shared.css';
    @import '$lib/styles/sandbox-settings.css';

    :global(.close-btn) {
        width: 28px;
        height: 28px;
        font-size: 1rem;
    }

    .locked-view {
        display: flex;
        flex-direction: column;
        gap: 12px;
    }

    .locked-notice {
        display: flex;
        align-items: flex-start;
        gap: 10px;
        padding: 10px 14px;
        background: rgba(210, 153, 34, 0.1);
        border: 1px solid rgba(210, 153, 34, 0.3);
        border-radius: var(--radius-md);
        color: var(--color-status-yellow, #d29922);
        font-size: 0.8125rem;
    }

    .locked-notice svg {
        flex-shrink: 0;
        margin-top: 1px;
    }

    .locked-notice p {
        margin: 0;
        line-height: 1.4;
    }

    .setting-label-group {
        display: flex;
        align-items: center;
        gap: 8px;
    }

    .config-options {
        display: flex;
        flex-direction: column;
        gap: 4px;
        padding: 12px;
        background: var(--color-surface-2);
        border-radius: var(--radius-md);
        margin-bottom: 16px;
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

    /* Expandable rows (subsection + non-subsection like Custom Mounts) */
    .config-row-subsection,
    .config-row-expandable {
        width: 100%;
        text-align: left;
        cursor: pointer;
        user-select: none;
    }

    .config-row-subsection:hover,
    .config-row-expandable:hover {
        background: var(--color-surface-3);
        border-radius: var(--radius-sm);
    }

    /* Wrapper for expandable row + content — extends highlight into parent padding */
    .config-row-group > .config-row {
        border-bottom: 1px solid var(--color-border);
    }

    .config-row-group.expanded {
        margin: 0 -12px;
        background: var(--color-surface-3);
        border-radius: var(--radius-sm);
    }

    .config-row-group.expanded > .config-row {
        padding-right: 12px;
        padding-left: 12px;
        border-bottom: none;
    }

    .config-row-group.expanded > .ports-content,
    .config-row-group.expanded > .mounts-content {
        padding-left: 12px;
        padding-right: 12px;
        border-bottom: 1px solid var(--color-border);
    }

    .chevron {
        margin-left: auto;
        flex-shrink: 0;
        color: var(--color-text-secondary);
        transition: transform 0.15s ease;
    }

    .chevron-open {
        transform: rotate(180deg);
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

    /* Every field in this form: one box, one focus ring, one disabled look. The width each one
       takes is the only thing that differs, so it is the only thing they declare for themselves. */
    .image-input,
    .memory-input,
    .port-input,
    .mount-input,
    .env-input {
        padding: 4px 8px;
        border-radius: var(--radius-sm);
        border: 1px solid var(--color-border);
        background: var(--color-surface-1);
        color: var(--color-text-primary);
        font-size: 0.75rem;
        font-family: var(--font-mono, monospace);
        transition: border-color 0.15s;
    }

    .image-input:focus,
    .memory-input:focus,
    .port-input:focus,
    .mount-input:focus,
    .env-input:focus {
        outline: none;
        border-color: var(--color-accent);
    }

    .image-input:disabled,
    .memory-input:disabled,
    .port-input:disabled,
    .mount-input:disabled,
    .env-input:disabled {
        opacity: 0.5;
    }

    /* The two number fields keep no spinners. */
    .memory-input,
    .port-input {
        -moz-appearance: textfield;
        appearance: textfield;
    }

    .memory-input::-webkit-outer-spin-button,
    .memory-input::-webkit-inner-spin-button,
    .port-input::-webkit-outer-spin-button,
    .port-input::-webkit-inner-spin-button {
        -webkit-appearance: none;
        margin: 0;
    }

    .image-input {
        flex-shrink: 0;
        width: 160px;
    }

    .memory-input {
        width: 80px;
    }

    /* Blank field = unlimited; the placeholder says so instead of showing 0. */
    .memory-input::placeholder {
        color: var(--color-text-secondary);
        opacity: 1;
    }

    .memory-input-group {
        display: flex;
        align-items: center;
        gap: 4px;
        flex-shrink: 0;
    }

    .memory-unit {
        font-size: 0.75rem;
        color: var(--color-text-secondary);
        white-space: nowrap;
    }

    .port-input {
        flex: 1;
        min-width: 120px;
        max-width: 200px;
    }

    .port-input-bind {
        max-width: 170px;
    }

    .mount-input {
        flex: 1;
        min-width: 100px;
        max-width: 180px;
    }

    .mount-input-host {
        max-width: 160px;
    }

    .env-input {
        flex: 1;
        min-width: 80px;
    }

    .env-input-key {
        max-width: 140px;
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
        background: var(--color-accent-dark);
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

    .radio-group-small {
        border-radius: var(--radius-sm);
    }

    .radio-group-small .radio-label {
        padding: 2px 7px;
        font-size: 0.6875rem;
    }

    /* Dockerfile section */
    .dockerfile-section {
        padding: 8px 0 0;
        padding-left: 36px;
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

    /* Build controls: cache modifier + build/rebuild button, right-aligned */
    .build-actions {
        display: flex;
        align-items: center;
        gap: 8px;
        flex-shrink: 0;
    }

    /* "Disable cache" checkbox next to the build button */
    .cache-toggle {
        display: flex;
        align-items: center;
        gap: 4px;
        font-size: 0.75rem;
        color: var(--color-text-secondary);
        cursor: pointer;
        white-space: nowrap;
        user-select: none;
    }

    .cache-toggle input[type="checkbox"] {
        margin: 0;
        cursor: pointer;
        accent-color: var(--color-accent);
    }

    .cache-toggle.disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }

    .cache-toggle.disabled input[type="checkbox"] {
        cursor: not-allowed;
    }

    .btn-build,
    .btn-rebuild,
    .btn-browse {
        display: flex;
        align-items: center;
        gap: 4px;
        border-radius: var(--radius-sm);
        font-size: 0.75rem;
        cursor: pointer;
        transition: background 0.15s, border-color 0.15s;
        white-space: nowrap;
    }

    .btn-build,
    .btn-rebuild {
        padding: 3px 8px;
    }

    .btn-rebuild,
    .btn-browse {
        border: 1px solid var(--color-border);
        background: var(--color-surface-1);
        color: var(--color-text-primary);
    }

    .btn-build {
        border: 1px solid var(--color-accent);
        background: var(--color-accent-dark);
        color: white;
    }

    .btn-build:hover:not(:disabled),
    .btn-browse:hover:not(:disabled),
    .btn-rebuild:hover {
        background: var(--color-surface-3);
        border-color: var(--color-accent);
    }

    .btn-build:hover:not(:disabled) {
        background: var(--color-accent);
    }

    .btn-build:disabled,
    .btn-rebuild:disabled,
    .btn-browse:disabled {
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
        padding: 5px 10px;
        flex-shrink: 0;
    }

    .image-tag-info {
        display: flex;
        align-items: center;
        gap: 6px;
        font-size: 0.75rem;
        color: var(--color-text-secondary);
        padding-left: 24px;
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

    .server-tool-notice {
        padding: 8px 12px;
        margin: -4px 0 12px;
        background: rgba(210, 153, 34, 0.08);
        border: 1px solid rgba(210, 153, 34, 0.3);
        border-radius: var(--radius-md);
        color: var(--color-text-secondary);
        font-size: 0.75rem;
        line-height: 1.45;
    }

    .save-warning-message {
        padding: 8px 12px;
        background: rgba(34, 197, 94, 0.08);
        border: 1px solid rgba(34, 197, 94, 0.3);
        border-radius: var(--radius-md);
        color: var(--color-status-green);
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

    .footer-wrapper {
        display: contents;
    }

    .spin {
        animation: spin 1s linear infinite;
    }

    /* Expandable content block */
    .ports-content,
    .mounts-content,
    .envs-content {
        padding: 0 0 4px;
    }

    .port-list,
    .mount-list,
    .env-list {
        display: flex;
        flex-direction: column;
        gap: 4px;
        padding-left: 36px;
        margin-top: 4px;
    }

    .port-item,
    .mount-item,
    .env-item {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 4px 8px;
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        cursor: pointer;
        transition: background 0.15s, border-color 0.15s;
    }

    .port-item:hover:not(:disabled),
    .mount-item:hover:not(:disabled),
    .env-item:hover:not(:disabled) {
        background: var(--color-surface-3);
    }

    .port-item.editing,
    .mount-item.editing,
    .env-item.editing {
        border-color: var(--color-accent);
        background: rgba(136, 192, 208, 0.05);
    }

    /* The saved value inside such a row, and the halves it is written in. */
    .port-mapping,
    .mount-info,
    .env-info {
        display: flex;
        align-items: center;
        gap: 6px;
        font-size: 0.75rem;
        font-family: var(--font-mono, monospace);
        color: var(--color-text-primary);
    }

    .mount-info,
    .env-info {
        min-width: 0;
    }

    /* A key = value row spends one step of the gap on the sign. */
    .env-info {
        gap: 4px;
    }

    .port-host,
    .port-container,
    .mount-path,
    .mount-container,
    .env-key {
        font-weight: 600;
    }

    .port-host,
    .mount-path,
    .env-key {
        color: var(--color-accent-light);
    }

    .mount-container,
    .env-value {
        color: var(--color-text-primary);
    }

    .port-container {
        color: white;
    }

    .mount-path,
    .mount-container,
    .env-key,
    .env-value {
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }

    .mount-path,
    .mount-container {
        min-width: 0;
    }

    .mount-actions {
        display: flex;
        align-items: center;
        gap: 6px;
        flex-shrink: 0;
    }

    .mount-arrow {
        color: var(--color-text-secondary);
        flex-shrink: 0;
    }

    .port-add-row,
    .mount-add-row,
    .env-add-row {
        display: flex;
        align-items: center;
        gap: 6px;
        padding-left: 36px;
    }

    .mount-add-row {
        margin: 4px 2px 0;
    }

    .env-add-row {
        margin-top: 4px;
    }

    /* The 28px square button of every add row: confirm, add, browse, cancel. */
    .btn-add-port,
    .btn-add-mount,
    .btn-add-env,
    .btn-browse-mount,
    .btn-cancel-edit {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 28px;
        height: 28px;
        border: 1px solid var(--color-border);
        background: var(--color-surface-1);
        cursor: pointer;
        border-radius: var(--radius-sm);
        flex-shrink: 0;
    }

    .btn-add-port,
    .btn-add-mount,
    .btn-add-env,
    .btn-browse-mount {
        color: var(--color-text-primary);
        transition: background 0.15s, border-color 0.15s;
    }

    .btn-add-port:hover:not(:disabled),
    .btn-add-mount:hover:not(:disabled),
    .btn-add-env:hover:not(:disabled),
    .btn-browse-mount:hover:not(:disabled) {
        background: var(--color-surface-3);
        border-color: var(--color-accent);
    }

    /* Something is typed but not added yet. */
    .btn-add-port.pending,
    .btn-add-mount.pending,
    .btn-add-env.pending {
        background: rgba(34, 197, 94, 0.15);
        border-color: rgba(34, 197, 94, 0.6);
        color: var(--color-status-green);
    }

    .btn-add-port:disabled,
    .btn-add-mount:disabled,
    .btn-browse-mount:disabled,
    .btn-cancel-edit:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }

    .btn-cancel-edit {
        color: var(--color-text-secondary);
        transition: background 0.15s, color 0.15s;
    }

    .btn-cancel-edit:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }

    /* The 20px discard control of every list row. */
    .btn-remove-port,
    .btn-remove-mount,
    .btn-remove-env {
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

    .btn-remove-port:hover:not(:disabled),
    .btn-remove-mount:hover:not(:disabled),
    .btn-remove-env:hover:not(:disabled) {
        background: var(--color-status-red-dark);
        color: white;
    }

    .btn-remove-port:disabled,
    .btn-remove-mount:disabled,
    .btn-remove-env:disabled {
        opacity: 0.4;
        cursor: not-allowed;
    }

    .port-hint {
        padding: 2px 0 0 36px;
        font-size: 0.6875rem;
        color: var(--color-text-muted);
    }

    .port-error,
    .mount-error,
    .env-error {
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
    }

    /* Capabilities section */
    .caps-content {
        padding: 12px 12px 4px;
        border-top: 1px solid var(--color-border);
    }

    .caps-input-wrapper {
        display: flex;
        flex-direction: column;
        gap: 6px;
    }

    /* Selected tags row */
    .caps-tags {
        display: flex;
        flex-wrap: wrap;
        gap: 4px;
    }

    .caps-tag {
        display: inline-flex;
        align-items: center;
        gap: 4px;
        padding: 2px 4px 2px 8px;
        background: rgba(136, 192, 208, 0.12);
        border: 1px solid rgba(136, 192, 208, 0.25);
        border-radius: var(--radius-sm);
        font-size: 0.75rem;
        font-family: var(--font-mono, monospace);
        color: var(--color-accent-light, #88c0d0);
    }

    .caps-tag-text {
        max-width: 180px;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }

    .caps-tag-remove {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 14px;
        height: 14px;
        padding: 0;
        border: none;
        background: transparent;
        color: inherit;
        cursor: pointer;
        border-radius: 50%;
        opacity: 0.6;
        transition: opacity 0.15s, background 0.15s;
    }

    .caps-tag-remove:hover {
        opacity: 1;
        background: rgba(136, 192, 208, 0.2);
    }

    /* Input field */
    .caps-input-field {
        position: relative;
    }

    .caps-input {
        width: 100%;
        padding: 6px 10px;
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        background: var(--color-surface-1);
        color: var(--color-text-primary);
        font-size: 0.8125rem;
        font-family: var(--font-mono, monospace);
        outline: none;
        transition: border-color 0.15s;
        box-sizing: border-box;
    }

    .caps-input::placeholder {
        color: var(--color-text-secondary);
        font-family: var(--font-sans, system-ui, sans-serif);
    }

    .caps-input:focus,
    .caps-input-field:focus .caps-input {
        border-color: var(--color-accent, #88c0d0);
    }

    .caps-input:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }

    /* Suggestions dropdown */
    .caps-dropdown {
        position: absolute;
        top: 100%;
        left: 0;
        right: 0;
        margin-top: 2px;
        max-height: 200px;
        overflow-y: auto;
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        box-shadow: 0 4px 12px rgba(0, 0, 0, 0.15);
        z-index: 100;
    }

    .caps-dropdown-item {
        display: flex;
        flex-direction: column;
        align-items: flex-start;
        width: 100%;
        padding: 6px 10px;
        border: none;
        background: transparent;
        color: var(--color-text-primary);
        cursor: pointer;
        text-align: left;
        transition: background 0.1s;
    }

    .caps-dropdown-item:hover,
    .caps-dropdown-item.selected {
        background: var(--color-surface-2);
    }

    .caps-dropdown-name {
        font-size: 0.8125rem;
        font-weight: 500;
        font-family: var(--font-mono, monospace);
    }

    .caps-dropdown-desc {
        font-size: 0.6875rem;
        color: var(--color-text-secondary);
    }

    @media (max-width: 768px) {
        .mount-input::placeholder,
        .port-input::placeholder {
            font-size: 12px;
        }
    }
</style>
