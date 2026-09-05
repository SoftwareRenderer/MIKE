<script lang="ts">
    import { onMount } from 'svelte';
    import { slide } from 'svelte/transition';
    import type { Project, Task } from '$lib/types';
    import { updateProject, buildSandboxImageStream, getBoard, getProject } from '$lib/api';
    import { formatMemoryLimitMB, memoryLimitToInput, parseMemoryLimitMB } from '$lib/utils/memory-stats';
    import { bindForInput, formatAssignedHost, formatForwardPort, formatForwardPorts, normalizeBind, portRowError } from '$lib/utils/sandbox-ports';
    import { dialogStore } from '$lib/stores';
    import type { PortForward, AssignedPort, SandboxMount, SandboxEnv } from '$lib/types';
    import Icon from '$lib/components/Icon.svelte';
    import shieldIcon from '$lib/assets/icons/shield.svg?raw';
    import shieldOffIcon from '$lib/assets/icons/shield-off.svg?raw';
    import BaseModal from './BaseModal.svelte';
import ToggleSwitch from './ToggleSwitch.svelte';
    import PathPicker from './PathPicker.svelte';

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
    const DEFAULT_MEMORY_LIMIT_MB = 1024;
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
    let capInputEl = $state<HTMLInputElement | null>(null);
    $effect(() => { sandboxCapabilities = project.sandbox_capabilities ?? []; });
    $effect(() => { capsExpanded = !!project.sandbox_capabilities && project.sandbox_capabilities.length > 0; });
    // Known capabilities for autocomplete suggestions
    const KNOWN_CAPABILITIES: Array<{ value: string; desc: string }> = [
        { value: 'CAP_CHOWN', desc: 'Change file ownership' },
        { value: 'CAP_DAC_OVERRIDE', desc: 'Bypass file permission checks' },
        { value: 'CAP_DAC_READ_SEARCH', desc: 'Bypass read permission on files/dirs' },
        { value: 'CAP_FOWNER', desc: 'Bypass permission checks requiring inode owner' },
        { value: 'CAP_FSETID', desc: "Don't clear setuid/setgid on file mod" },
        { value: 'CAP_IPC_LOCK', desc: 'Lock memory' },
        { value: 'CAP_IPC_OWNER', desc: 'Bypass IPC ownership checks' },
        { value: 'CAP_KILL', desc: 'Bypass permission checks for signals' },
        { value: 'CAP_LEASE', desc: 'Lease files' },
        { value: 'CAP_LINUX_IMMUTABLE', desc: 'Set immutable file attribute' },
        { value: 'CAP_MKNOD', desc: 'Create special files' },
        { value: 'CAP_NET_ADMIN', desc: 'Perform network-related operations' },
        { value: 'CAP_NET_BIND_SERVICE', desc: 'Bind to ports < 1024' },
        { value: 'CAP_NET_BROADCAST', desc: 'Make socket broadcasts' },
        { value: 'CAP_NET_RAW', desc: 'Use RAW and PACKET sockets' },
        { value: 'CAP_SETFCAP', desc: 'Set file capabilities' },
        { value: 'CAP_SETGID', desc: 'Manipulate process group IDs' },
        { value: 'CAP_SETPCAP', desc: 'Transfer capabilities' },
        { value: 'CAP_SETUID', desc: 'Manipulate process user IDs' },
        { value: 'CAP_SYS_CHROOT', desc: 'Use chroot()' },
        { value: 'CAP_SYS_PTRACE', desc: 'Trace arbitrary processes' },
        { value: 'CAP_SYS_ADMIN', desc: 'Perform a wide range of system admin operations' },
        { value: 'CAP_SYS_BOOT', desc: 'Reboot the system' },
        { value: 'CAP_SYS_MODULE', desc: 'Load/unload kernel modules' },
        { value: 'CAP_SYS_NICE', desc: 'Raise process nice priority' },
        { value: 'CAP_SYS_RAWIO', desc: 'Perform raw I/O ports' },
        { value: 'CAP_SYS_RESOURCE', desc: 'Override resource limits' },
        { value: 'CAP_SYS_TIME', desc: 'Set system clock' },
        { value: 'CAP_SYS_TTY_CONFIG', desc: 'Configure TTY devices' },
        { value: 'CAP_BPF', desc: 'Use BPF operations' },
        { value: 'CAP_PERFMON', desc: 'Use performance monitoring' },
        { value: 'CAP_AUDIT_CONTROL', desc: 'Modify kernel audit configuration' },
        { value: 'CAP_AUDIT_READ', desc: 'Read kernel audit log' },
        { value: 'CAP_AUDIT_WRITE', desc: 'Write records to kernel audit log' },
        { value: 'CAP_BLOCK_SUSPEND', desc: 'Use suspend-to-memory/disk' },
        { value: 'CAP_CHECKPOINT_RESTORE', desc: 'Checkpoint and restore' },
        { value: 'CAP_MAC_ADMIN', desc: 'Override MAC configuration' },
        { value: 'CAP_MAC_OVERRIDE', desc: 'Override MAC enforcement' },
        { value: 'CAP_WAKE_ALARM', desc: 'Trigger wake alarms' },
        { value: 'CAP_SYSLOG', desc: 'Perform syslog operations' },
    ];
    // Filtered suggestions based on input
    let capSuggestions = $derived.by(() => {
        const query = capInputValue.trim().toUpperCase();
        if (!query) return [];
        return KNOWN_CAPABILITIES.filter(c =>
            !sandboxCapabilities.includes(c.value) &&
            c.value.toUpperCase().includes(query)
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
            mountError = 'Host path is required';
            return;
        }
        if (!newMountContainerPath.trim()) {
            mountError = 'Container path is required';
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
            envError = 'Environment variable name is required';
            return;
        }
        // Check for duplicate keys
        const isDuplicate = sandboxEnvs.some((e, i) => 
            i !== editingEnvIndex && e.key === newEnvKey.trim()
        );
        if (isDuplicate) {
            envError = 'Duplicate variable name: ' + newEnvKey.trim();
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
                capInputValue = capSuggestions[capSelectedIndex].value;
            }
        } else if (e.key === 'Enter' && capInputValue.trim()) {
            e.preventDefault();
            // If a suggestion is selected, use it; otherwise add the raw input
            if (capSelectedIndex >= 0 && capSuggestions[capSelectedIndex]) {
                addCapability(capSuggestions[capSelectedIndex].value);
            } else {
                addCapability(capInputValue);
            }
        } else if (e.key === 'ArrowDown' && capSuggestions.length > 0) {
            e.preventDefault();
            capSelectedIndex = capSelectedIndex < 0 ? 0 : (capSelectedIndex + 1) % capSuggestions.length;
            capInputValue = capSuggestions[capSelectedIndex].value;
        } else if (e.key === 'ArrowUp' && capSuggestions.length > 0) {
            e.preventDefault();
            capSelectedIndex = capSelectedIndex < 0 ? capSuggestions.length - 1 : (capSelectedIndex - 1 + capSuggestions.length) % capSuggestions.length;
            capInputValue = capSuggestions[capSelectedIndex].value;
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
            sandbox_envs?: SandboxEnv[];
            sandbox_capabilities?: string[];
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
            const confirmed = await dialogStore.showConfirm({
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

<BaseModal title="Sandbox Configuration" width={isLocked ? '480px' : '560px'} footer={isLocked ? undefined : footer} onClose={onClose}>
    {#if isLocked}
        <!-- Read-only view shown when tasks are in progress -->
        <div class="locked-view">
            <div class="locked-notice">
                <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                    <rect x="3" y="7" width="10" height="7" rx="1.5" stroke="currentColor" stroke-width="1.3"/>
                    <path d="M5.5 7V5a2.5 2.5 0 015 0v2" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"/>
                </svg>
                <p>Cannot change sandbox settings while tasks are In Progress. The settings below are shown for reference only.</p>
            </div>

            <div class="settings-list">
                <!-- Sandbox enabled -->
                <div class="setting-row">
                    <div class="setting-label-group">
                        <span class="setting-label">Sandbox Isolation</span>
                    </div>
                    <span class="setting-value">{sandboxEnabled ? 'Enabled' : 'Disabled'}</span>
                </div>

                {#if sandboxEnabled}
                <!-- Network isolation -->
                <div class="setting-row">
                    <span class="setting-label">Network Isolation</span>
                    <span class="setting-value">{networkIsolation ? 'Enabled' : 'Disabled'}</span>
                </div>

                <!-- Memory limit -->
                <div class="setting-row">
                    <span class="setting-label">Memory Limit</span>
                    <span class="setting-value">{formatMemoryLimitMB(sandboxMemoryLimit)}</span>
                </div>

                <!-- Image -->
                <div class="setting-row">
                    <span class="setting-label">Image</span>
                    <span class="setting-value setting-value-mono">{sandboxDockerfilePath || sandboxImage || 'Default'}</span>
                </div>

                <!-- Forward ports -->
                {#if forwardPorts.length > 0}
                <div class="setting-row">
                    <span class="setting-label">Forwarded Ports</span>
                    <span class="setting-value">{formatForwardPorts(forwardPorts)}</span>
                </div>

                <!-- Active port mappings -->
                {#if hasActivePorts}
                <div class="active-ports-section">
                    <span class="section-label">Active Port Mappings</span>
                    <div class="active-ports-list">
                        {#each forwardPorts as port}
                        {#if activeHostPorts.has(port.container_port)}
                        <div class="active-port-item">
                            <span class="active-port-host" title="Host port">{formatAssignedHost(activeHostPorts.get(port.container_port) ?? 0, port.host_ip)}</span>
                            <svg width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden="true">
                                <path d="M4 3L8 6L4 9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                            </svg>
                            <span class="active-port-container" title="Container port">{port.container_port}</span>
                        </div>
                        {/if}
                        {/each}
                    </div>
                </div>
                {/if}
                {/if}

                <!-- Custom mounts -->
                {#if sandboxMounts.length > 0}
                <div class="locked-mounts-section">
                    <span class="section-label">Custom Mounts</span>
                    <div class="locked-mounts-list">
                        {#each sandboxMounts as mount}
                        <div class="locked-mount-item">
                            <span class="locked-mount-host" title={mount.host_path}>{mount.host_path}</span>
                            <svg width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden="true">
                                <path d="M4 3L8 6L4 9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                            </svg>
                            <span class="locked-mount-container" title={mount.container_path}>{mount.container_path}</span>
                            <span class="locked-mount-mode" class:mode-rw={mount.mode === 'rw'} class:mode-ro={mount.mode === 'ro'} title={mount.mode === 'rw' ? 'Read/Write' : 'Read-only'}>{mount.mode}</span>
                        </div>
                        {/each}
                    </div>
                </div>
                {/if}

                <!-- Environment variables -->
                {#if sandboxEnvs.length > 0}
                <div class="locked-envs-section">
                    <span class="section-label">Environment Variables</span>
                    <div class="locked-envs-list">
                        {#each sandboxEnvs as env}
                        <div class="locked-env-item">
                            <span class="locked-env-key" title={env.key}>{env.key}</span>
                            <span class="locked-env-equals">=</span>
                            <span class="locked-env-value" title={env.value}>{env.value || '(empty)'}</span>
                        </div>
                        {/each}
                    </div>
                </div>
                {/if}

                <!-- Capabilities -->
                {#if sandboxCapabilities.length > 0}
                <div class="locked-caps-section">
                    <span class="section-label">Capabilities</span>
                    <div class="locked-caps-list">
                        {#each sandboxCapabilities as cap}
                        <div class="locked-cap-item">
                            <span class="locked-cap-value" title={cap}>{cap}</span>
                        </div>
                        {/each}
                    </div>
                </div>
                {/if}
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
            <div class="config-row-group" class:expanded={portsExpanded}>
            <button type="button" class="config-row config-row-subsection" onclick={() => { portsExpanded = !portsExpanded; }} aria-expanded={portsExpanded} aria-label="Toggle port forwarding settings">
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
                        <span class="port-host" title="Host port">{activeHostPorts.get(port.container_port)}</span>
                        <svg width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden="true">
                            <path d="M4 3L8 6L4 9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                        </svg>
                        {/if}
                        <span class="port-container" title="Container port">{formatForwardPort(port)}</span>
                    </span>
                    <button type="button" class="btn-remove-port" onclick={(e) => { e.stopPropagation(); removePortForward(i); }} disabled={isLocked} title="Remove">
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
                {#if editingPortIndex !== null}
                    <button type="button" class="btn-cancel-edit" onclick={cancelPortEdit} title="Cancel edit">
                        <svg width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden="true">
                            <path d="M2 2L10 10M10 2L2 10" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"/>
                        </svg>
                    </button>
                {/if}
                <input type="text" class="port-input port-input-bind" bind:value={newHostIP} placeholder="Host interface" spellcheck="false" autocomplete="off" disabled={isLocked} aria-label="Host interface (IP)" />
                <input type="number" class="port-input" bind:value={newContainerPort} placeholder="Container port" min="1" max="65535" disabled={isLocked} aria-label="Container port" />
                <button type="button" class="btn-add-port" class:pending={hasPendingPort} onclick={addPortForward} disabled={isLocked} title={editingPortIndex !== null ? 'Save port' : 'Add port forward'}>
                    {#if editingPortIndex !== null}
                        <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
                            <path d="M3 8L6 11L11 6" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                        </svg>
                    {:else}
                        <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
                            <path d="M7 2V12M2 7H12" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
                        </svg>
                    {/if}
                </button>
            </div>

            {#if portError}
            <div class="port-error">{portError}</div>
            {:else}
            <div class="port-hint">Host Interface (Optional): Bind to specific host interface. Leave empty to bind to all.</div>
            {/if}
            </div>
            {/if}
            </div>
            {/if}

            <!-- Custom mounts (collapsible) -->
            <div class="config-row-group" class:expanded={mountsExpanded}>
            <button type="button" class="config-row config-row-expandable" onclick={() => { mountsExpanded = !mountsExpanded; }} aria-expanded={mountsExpanded} aria-label="Toggle mount settings">
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
                        <span class="mount-mode" class:mode-rw={mount.mode === 'rw'} class:mode-ro={mount.mode === 'ro'} title={mount.mode === 'rw' ? 'Mount Read/Write' : 'Mount Read-only'}>{mount.mode}</span>
                        <button type="button" class="btn-remove-mount" onclick={(e) => { e.stopPropagation(); removeMount(i); }} disabled={isLocked} title="Remove">
                        <svg width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden="true">
                            <path d="M3 3L9 9M9 3L3 9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"/>
                        </svg>
                    </button>
                    </div>
                </div>
                {/each}
            </div>
            {/if}

            <!-- Add new mount -->
            <div class="mount-add-row">
                {#if editingMountIndex !== null}
                    <button type="button" class="btn-cancel-edit" onclick={cancelMountEdit} title="Cancel edit">
                        <svg width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden="true">
                            <path d="M2 2L10 10M10 2L2 10" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"/>
                        </svg>
                    </button>
                {/if}
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
                    <label class="radio-label" class:active={newMountMode === 'rw'} title="Mount Read/Write">
                        <input type="radio" name="mount-mode" value="rw" checked={newMountMode === 'rw'} onchange={() => newMountMode = 'rw'} disabled={isLocked} />
                        <span class="radio-text">rw</span>
                    </label>
                    <label class="radio-label" class:active={newMountMode === 'ro'} title="Mount Read-only">
                        <input type="radio" name="mount-mode" value="ro" checked={newMountMode === 'ro'} onchange={() => newMountMode = 'ro'} disabled={isLocked} />
                        <span class="radio-text">ro</span>
                    </label>
                </div>
                <button type="button" class="btn-add-mount" class:pending={hasPendingMount} onclick={addMount} disabled={isLocked} title={editingMountIndex !== null ? 'Save mount' : 'Add mount'}>
                    {#if editingMountIndex !== null}
                        <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
                            <path d="M3 8L6 11L11 6" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                        </svg>
                    {:else}
                        <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
                            <path d="M7 2V12M2 7H12" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
                        </svg>
                    {/if}
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
            <button type="button" class="config-row config-row-expandable" onclick={() => { envsExpanded = !envsExpanded; }} aria-expanded={envsExpanded} aria-label="Toggle environment variables settings">
                <div class="config-info">
                    <div class="config-icon">
                        <svg width="20" height="20" viewBox="0 0 20 20" fill="none" aria-hidden="true">
                            <path d="M3 6h14M3 10h14M3 14h10" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"/>
                        </svg>
                    </div>
                    <div class="config-text">
                        <span class="config-label" class:collapsed={!envsExpanded}>Environment Variables</span>
                        {#if envsExpanded}
                        <span class="config-desc">Set environment variables in sandbox containers.</span>
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
                        <span class="env-equals">=</span>
                        <span class="env-value" title={env.value}>{env.value || '(empty)'}</span>
                    </div>
                    <button type="button" class="btn-remove-env" onclick={(e) => { e.stopPropagation(); removeEnv(i); }} disabled={isLocked} title="Remove">
                        <svg width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden="true">
                            <path d="M3 3L9 9M9 3L3 9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"/>
                        </svg>
                    </button>
                </div>
                {/each}
            </div>
            {/if}

            <!-- Add new env var -->
            <div class="env-add-row">
                {#if editingEnvIndex !== null}
                    <button type="button" class="btn-cancel-edit" onclick={cancelEnvEdit} title="Cancel edit">
                        <svg width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden="true">
                            <path d="M2 2L10 10M10 2L2 10" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"/>
                        </svg>
                    </button>
                {/if}
                <input type="text" class="env-input env-input-key" bind:value={newEnvKey} placeholder="KEY" disabled={isLocked} aria-label="Variable name" />
                <span class="env-equals-sign">=</span>
                <input type="text" class="env-input" bind:value={newEnvValue} placeholder="value" disabled={isLocked} aria-label="Variable value" />
                <button type="button" class="btn-add-env" class:pending={hasPendingEnv} onclick={addEnv} disabled={isLocked} title={editingEnvIndex !== null ? 'Save variable' : 'Add variable'}>
                    {#if editingEnvIndex !== null}
                        <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
                            <path d="M3 8L6 11L11 6" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                        </svg>
                    {:else}
                        <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
                            <path d="M7 2V12M2 7H12" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
                        </svg>
                    {/if}
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
            <button type="button" class="config-row config-row-expandable" onclick={() => { capsExpanded = !capsExpanded; }} aria-expanded={capsExpanded} aria-label="Toggle capabilities settings">
                <div class="config-info">
                    <div class="config-icon">
                        <svg width="20" height="20" viewBox="0 0 20 20" fill="none" aria-hidden="true">
                            <path d="M10 2l2.2 4.5 5 .7-3.6 3.5.9 5L10 13.3 5.5 15.7l.9-5L2.8 7.2l5-.7L10 2z" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round"/>
                        </svg>
                    </div>
                    <div class="config-text">
                        <span class="config-label" class:collapsed={!capsExpanded}>Capabilities</span>
                        {#if capsExpanded}
                        <span class="config-desc">Linux capabilities to add to the container. By default, all capabilities are dropped (--cap-drop=ALL).</span>
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
                        <button type="button" class="caps-tag-remove" onclick={() => removeCapability(cap)} title="Remove {cap}" aria-label="Remove {cap}">
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
                        bind:this={capInputEl}
                        placeholder={sandboxCapabilities.length === 0 ? 'Type capability name, e.g. CAP_NET_RAW' : 'Add more...'}
                        disabled={isLocked}
                        autocomplete="off"
                        spellcheck="false"
                        aria-label="Add capability"
                        role="combobox"
                        aria-expanded={capDropdownOpen}
                        aria-controls="caps-dropdown-list"
                    />
                    <!-- Suggestions dropdown -->
                    {#if capDropdownOpen && capSuggestions.length > 0}
                    <div id="caps-dropdown-list" class="caps-dropdown" role="listbox">
                        {#each capSuggestions as suggestion, i}
                        <button type="button" class="caps-dropdown-item" class:selected={i === capSelectedIndex} role="option" aria-selected={i === capSelectedIndex} onclick={() => addCapability(suggestion.value)}>
                            <span class="caps-dropdown-name">{suggestion.value}</span>
                            <span class="caps-dropdown-desc">{suggestion.desc}</span>
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
                        <span class="config-label">Memory Limit</span>
                        <span class="config-desc">Max container memory (MB) — blank = no limit</span>
                    </div>
                </div>
                <div class="memory-input-group">
                    <input type="number" class="memory-input" bind:value={sandboxMemoryLimit} min="0" step="256" placeholder="No limit" disabled={isLocked} aria-label="Maximum container memory in MB" />
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
                        <span class="config-desc">Image for sandbox containers — must name its registry, e.g. docker.io/library/ubuntu:24.04</span>
                    </div>
                </div>
                <input type="text" class="image-input" bind:value={sandboxImage} placeholder="docker.io/ubuntu:latest" disabled={isLocked} aria-label="Sandbox image name" />
            </div>
            {:else}
            <!-- Dockerfile section -->
            <div class="dockerfile-section">
                <div class="dockerfile-header">
                    <div class="config-text">
                        <span class="config-label">Dockerfile</span>
                        <span class="config-desc">Select a Dockerfile on the server</span>
                    </div>
                    {#if sandboxDockerfilePath}
                    <div class="build-actions">
                        <!-- Applies to the next build started here, not a saved setting -->
                        <label class="cache-toggle" class:disabled={isLocked || building}
                               title="Skip the container build layer cache for the next build">
                            <input type="checkbox" aria-label="Disable build cache" bind:checked={buildNoCache} disabled={isLocked || building} />
                            <span class="cache-toggle-text">Disable cache</span>
                        </label>
                        {#if dockerfileChanged}
                        <button type="button" class="btn-build" onclick={handleRebuild} disabled={isLocked || building} title="Build image">
                            {#if building}
                                <svg class="spin" width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
                                    <circle cx="7" cy="7" r="5" stroke="currentColor" stroke-width="1.5" stroke-dasharray="20 10" stroke-linecap="round"/>
                                </svg>
                            {:else}
                                <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
                                    <path d="M7 2L2 12h10L7 2z" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round" fill="none"/>
                                </svg>
                            {/if}
                            {building ? 'Building...' : 'Build'}
                        </button>
                        {:else}
                        <button type="button" class="btn-rebuild" onclick={handleRebuild} disabled={isLocked || building} title={buildNoCache ? 'Rebuild image without cache' : 'Rebuild image'}>
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
    {/if}
</BaseModal>

{#snippet footer()}
    <div class="footer-wrapper">
        <button type="button" class="btn-secondary" onclick={onClose} disabled={saving}>Cancel</button>
        <button type="button" class="btn-primary" onclick={handleSave} disabled={saving || isLocked}>
            {saving ? 'Saving...' : 'Save'}
        </button>
    </div>
{/snippet}

{#if showDockerfilePicker}
    <PathPicker title="Select Dockerfile" initialPath={project.path} emptyLabel="No Dockerfiles found in this directory" selectTitle="Select this Dockerfile" fileFilter={isDockerfile} onClose={handleDockerfilePickerClose} onSelect={handleDockerfileSelected} />
{/if}

{#if showDirectoryPicker}
    <PathPicker title="Select Directory" initialPath="/home" emptyLabel="No subdirectories in this path" selectTitle="Select this directory" onClose={() => showDirectoryPicker = false} onSelect={(path: string) => { newMountHostPath = path; showDirectoryPicker = false; }} />
{/if}

<style>
    @import '$lib/styles/shared.css';

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

    /* Read-only settings list */
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

    .setting-label-group {
        display: flex;
        align-items: center;
        gap: 8px;
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

    .section-label {
        font-size: 0.75rem;
        font-weight: 500;
        color: var(--color-text-secondary);
        text-transform: uppercase;
        letter-spacing: 0.3px;
    }

    /* Active port mappings in locked view */
    .active-ports-section {
        margin-top: 4px;
        padding-top: 8px;
        border-top: 1px solid var(--color-border);
        display: flex;
        flex-direction: column;
        gap: 6px;
    }

    .active-ports-list {
        display: flex;
        flex-direction: column;
        gap: 4px;
    }

    .active-port-item {
        display: flex;
        align-items: center;
        gap: 6px;
        padding: 4px 8px;
        background: var(--color-surface-2);
        border-radius: var(--radius-sm);
        font-size: 0.75rem;
        font-family: var(--font-mono, monospace);
    }

    .active-port-host {
        font-weight: 600;
        color: var(--color-accent-light);
    }

    .active-port-container {
        color: var(--color-text-primary);
    }

    /* Custom mounts in locked view */
    .locked-mounts-section {
        display: flex;
        flex-direction: column;
        gap: 6px;
        margin-top: 4px;
        padding-top: 8px;
        border-top: 1px solid var(--color-border);
    }

    .locked-mounts-list {
        display: flex;
        flex-direction: column;
        gap: 4px;
    }

    .locked-mount-item {
        display: flex;
        align-items: center;
        gap: 6px;
        padding: 4px 8px;
        background: var(--color-surface-2);
        border-radius: var(--radius-sm);
        font-size: 0.75rem;
        font-family: var(--font-mono, monospace);
    }

    .locked-mount-host {
        font-weight: 600;
        color: var(--color-accent-light);
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }

    .locked-mount-container {
        color: var(--color-text-primary);
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }

    .locked-mount-mode {
        margin-left: auto;
        flex-shrink: 0;
        font-size: 0.6875rem;
        padding: 1px 5px;
        border-radius: 3px;
        background: var(--color-surface-3);
        color: var(--color-text-secondary);
        font-weight: 600;
    }

    .locked-mount-mode.mode-rw {
        background: rgba(248, 81, 73, 0.15);
        color: var(--color-status-red);
    }

    .locked-mount-mode.mode-ro {
        background: rgba(210, 153, 34, 0.15);
        color: var(--color-status-yellow, #d29922);
    }

    /* Environment variables in locked view */
    .locked-envs-section {
        display: flex;
        flex-direction: column;
        gap: 6px;
        margin-top: 4px;
        padding-top: 8px;
        border-top: 1px solid var(--color-border);
    }

    .locked-envs-list {
        display: flex;
        flex-direction: column;
        gap: 4px;
    }

    .locked-env-item {
        display: flex;
        align-items: center;
        gap: 4px;
        padding: 4px 8px;
        background: var(--color-surface-2);
        border-radius: var(--radius-sm);
        font-size: 0.75rem;
        font-family: var(--font-mono, monospace);
    }

    .locked-env-key {
        font-weight: 600;
        color: var(--color-accent-light);
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }

    .locked-env-equals {
        color: var(--color-text-secondary);
        flex-shrink: 0;
    }

    .locked-env-value {
        color: var(--color-text-primary);
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }

    /* Capabilities in locked view */
    .locked-caps-section {
        display: flex;
        flex-direction: column;
        gap: 6px;
        margin-top: 4px;
        padding-top: 8px;
        border-top: 1px solid var(--color-border);
    }

    .locked-caps-list {
        display: flex;
        flex-wrap: wrap;
        gap: 4px;
    }

    .locked-cap-item {
        padding: 2px 8px;
        background: rgba(136, 192, 208, 0.12);
        border: 1px solid rgba(136, 192, 208, 0.25);
        border-radius: var(--radius-sm);
        font-size: 0.75rem;
        font-family: var(--font-mono, monospace);
    }

    .locked-cap-value {
        color: var(--color-accent-light, #88c0d0);
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

    /* Blank field = unlimited; the placeholder says so instead of showing 0. */
    .memory-input::placeholder {
        color: var(--color-text-secondary);
        opacity: 1;
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

    .btn-build {
        display: flex;
        align-items: center;
        gap: 4px;
        padding: 3px 8px;
        border-radius: var(--radius-sm);
        border: 1px solid var(--color-accent);
        background: var(--color-accent-dark);
        color: white;
        font-size: 0.75rem;
        cursor: pointer;
        transition: background 0.15s, border-color 0.15s;
        white-space: nowrap;
    }

    .btn-build:hover:not(:disabled) {
        background: var(--color-accent);
    }

    .btn-build:disabled {
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
    @keyframes spin {
        from { transform: rotate(0deg); }
        to { transform: rotate(360deg); }
    }

    .spin {
        animation: spin 1s linear infinite;
    }

    /* Expandable content block (ports + mounts) */
    .ports-content,
    .mounts-content {
        padding: 0 0 4px;
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
        cursor: pointer;
        transition: background 0.15s, border-color 0.15s;
    }

    .port-item:hover:not(:disabled) {
        background: var(--color-surface-3);
    }

    .port-item.editing {
        border-color: var(--color-accent);
        background: rgba(136, 192, 208, 0.05);
    }

    .port-mapping {
        display: flex;
        align-items: center;
        gap: 6px;
        font-size: 0.75rem;
        font-family: var(--font-mono, monospace);
        color: var(--color-text-primary);
    }

    .port-host,
    .port-container {
        font-weight: 600;
        color: var(--color-accent-light);
    }
    .port-container {
        color: white;
    }

    /* Remove buttons (ports + mounts) */
    .btn-remove-port,
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

    .btn-remove-port:hover:not(:disabled),
    .btn-remove-mount:hover:not(:disabled) {
        background: var(--color-status-red-dark);
        color: white;
    }

    .btn-remove-port:disabled,
    .btn-remove-mount:disabled {
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

    .port-input-bind {
        max-width: 170px;
    }

    .port-hint {
        padding: 2px 0 0 36px;
        font-size: 0.6875rem;
        color: var(--color-text-muted);
    }

    /* Add buttons (ports + mounts) */
    .btn-add-port,
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

    .btn-add-port:hover:not(:disabled),
    .btn-add-mount:hover:not(:disabled) {
        background: var(--color-surface-3);
        border-color: var(--color-accent);
    }

    .btn-add-port.pending,
    .btn-add-mount.pending {
        background: rgba(34, 197, 94, 0.15);
        border-color: rgba(34, 197, 94, 0.6);
        color: var(--color-status-green);
    }

    .btn-add-port:disabled,
    .btn-add-mount:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }

    /* Error messages (ports + mounts) */
    .port-error,
    .mount-error {
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

    /* Mounts */
    .mount-list {
        display: flex;
        flex-direction: column;
        gap: 4px;
        margin-top: 4px;
        padding-left: 36px;
    }

    .mount-item {
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

    .mount-item:hover:not(:disabled) {
        background: var(--color-surface-3);
    }

    .mount-item.editing {
        border-color: var(--color-accent);
        background: rgba(136, 192, 208, 0.05);
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
        color: var(--color-accent-light);
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
        min-width: 0;
    }

    .mount-container {
        font-weight: 600;
        color: var(--color-text-primary);
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
        min-width: 0;
    }

    .mount-actions {
        display: flex;
        align-items: center;
        gap: 6px;
        flex-shrink: 0;
    }

    .mount-mode {
        font-size: 0.6875rem;
        padding: 1px 5px;
        border-radius: 3px;
        background: var(--color-surface-3);
        color: var(--color-text-secondary);
        font-weight: 600;
    }

    .mount-mode.mode-ro {
        background: rgba(210, 153, 34, 0.15);
        color: var(--color-status-yellow, #d29922);
    }

    .mount-mode.mode-rw {
        background: rgba(248, 81, 73, 0.15);
        color: var(--color-status-red);
    }

    .mount-add-row {
        display: flex;
        align-items: center;
        gap: 6px;
        margin: 4px 2px 0;
        padding-left: 36px;
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

    /* Environment variables */
    .envs-content {
        padding: 0 0 4px;
    }

    .env-list {
        display: flex;
        flex-direction: column;
        gap: 4px;
        margin-top: 4px;
        padding-left: 36px;
    }

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

    .env-item:hover:not(:disabled) {
        background: var(--color-surface-3);
    }

    .env-item.editing {
        border-color: var(--color-accent);
        background: rgba(136, 192, 208, 0.05);
    }

    .env-info {
        display: flex;
        align-items: center;
        gap: 4px;
        font-size: 0.75rem;
        font-family: var(--font-mono, monospace);
        color: var(--color-text-primary);
        min-width: 0;
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

    .btn-remove-env:hover:not(:disabled) {
        background: var(--color-status-red-dark);
        color: white;
    }

    .btn-remove-env:disabled {
        opacity: 0.4;
        cursor: not-allowed;
    }

    .env-add-row {
        display: flex;
        align-items: center;
        gap: 6px;
        margin-top: 4px;
        padding-left: 36px;
    }

    .env-input {
        flex: 1;
        min-width: 80px;
        padding: 4px 8px;
        border-radius: var(--radius-sm);
        border: 1px solid var(--color-border);
        background: var(--color-surface-1);
        color: var(--color-text-primary);
        font-size: 0.75rem;
        font-family: var(--font-mono, monospace);
        transition: border-color 0.15s;
    }

    .env-input-key {
        max-width: 140px;
    }

    .env-input:focus {
        outline: none;
        border-color: var(--color-accent);
    }

    .env-input:disabled {
        opacity: 0.5;
    }

    .env-equals-sign {
        color: var(--color-text-secondary);
        flex-shrink: 0;
        font-size: 0.75rem;
    }

    .btn-add-env {
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

    .btn-add-env:hover:not(:disabled) {
        background: var(--color-surface-3);
        border-color: var(--color-accent);
    }

    .btn-add-env.pending {
        background: rgba(34, 197, 94, 0.15);
        border-color: rgba(34, 197, 94, 0.6);
        color: var(--color-status-green);
    }

    .btn-cancel-edit {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 28px;
        height: 28px;
        border: 1px solid var(--color-border);
        background: var(--color-surface-1);
        color: var(--color-text-secondary);
        cursor: pointer;
        border-radius: var(--radius-sm);
        transition: background 0.15s, color 0.15s;
        flex-shrink: 0;
    }

    .btn-cancel-edit:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }

    .btn-cancel-edit:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }

    .env-error {
        padding: 4px 8px;
        padding-left: 36px;
        background: rgba(248, 81, 73, 0.1);
        border: 1px solid rgba(248, 81, 73, 0.3);
        border-radius: var(--radius-sm);
        color: var(--color-status-red);
        font-size: 0.75rem;
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
