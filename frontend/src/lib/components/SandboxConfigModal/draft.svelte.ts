import { getBoard, getProject, updateProject, buildSandboxImageStream } from '$lib/api';
import { t } from '$lib/i18n';
import type { AssignedPort, PortForward, Project, SandboxEnv, SandboxMount, Task } from '$lib/types';
import { bindForInput, normalizeBind, portRowError } from '$lib/utils/sandbox-ports';
import { capSuggestionsFor, normalizeCapability } from './capabilities';
import { draftFromProject, type ImageSource, type SandboxDraft } from './drafts';

/**
 * The one reactive holder of a project's unsaved sandbox configuration. The modal constructs it when
 * the screen opens, every section reads and edits it, and nothing else holds sandbox configuration
 * state — one Save turns it into one `PUT` (`drafts.ts` owns that comparison).
 *
 * It writes no rule the server does not also enforce: the field checks here are pre-validation of
 * what `types.ValidateSandboxAdditions` decides on `PUT` and `sandbox.Create` refuses again at
 * container creation, so a refusal here is a courtesy and never the only check.
 */
export class SandboxDraftOwner {
    // ── The edited values ────────────────────────────────────────────────────
    sandboxEnabled = $state(false);
    networkIsolation = $state(false);
    /** What the isolation switch is measured against — see `SandboxDraft.networkIsolationBaseline`. */
    networkIsolationBaseline = $state(false);
    memoryLimitMB = $state<number | null>(null);
    image = $state('');
    dockerfilePath = $state('');
    imageSource = $state<ImageSource>('image');
    forwardPorts = $state<PortForward[]>([]);
    mounts = $state<SandboxMount[]>([]);
    envs = $state<SandboxEnv[]>([]);
    capabilities = $state<string[]>([]);

    // ── Which sections are open ──────────────────────────────────────────────
    portsExpanded = $state(false);
    mountsExpanded = $state(false);
    envsExpanded = $state(false);
    capsExpanded = $state(false);

    // ── The row each section is currently adding or editing ──────────────────
    editingPortIndex = $state<number | null>(null);
    newContainerPort = $state('');
    newHostIP = $state('');
    portError = $state('');
    editingMountIndex = $state<number | null>(null);
    newMountHostPath = $state('');
    newMountContainerPath = $state('');
    newMountMode = $state<'rw' | 'ro'>('rw');
    mountError = $state('');
    editingEnvIndex = $state<number | null>(null);
    newEnvKey = $state('');
    newEnvValue = $state('');
    envError = $state('');

    // ── The capability input ─────────────────────────────────────────────────
    capInputValue = $state('');
    capDropdownOpen = $state(false);
    capSelectedIndex = $state(-1);

    /** Something is typed in an add field but not added: saving would drop it. */
    saveWarning = $state('');

    // ── What the container is publishing right now ───────────────────────────
    /** container_port → host_port, across the project's terminal and every running task. */
    activeHostPorts = $state<Map<number, number>>(new Map());
    terminalPorts = $state<AssignedPort[]>([]);
    hasActivePorts = $derived(this.activeHostPorts.size > 0);

    // ── The image build this screen can start ────────────────────────────────
    building = $state(false);
    /** Builds started from here skip the layer cache. Not a setting — never saved. */
    buildNoCache = $state(false);
    buildError = $state('');
    buildLog = $state<string[]>([]);

    /** The toggle's previous answer, so only a real off→on flip auto-answers isolation. */
    private prevSandboxEnabled = false;
    /** The first seed reads the row rather than flipping anything: an opening is not a toggle. */
    private unreadRow = true;
    /** Whether the person has touched the isolation switch at all. */
    private userToggledNetwork = false;

    /** Start over from the stored row: on open, and whenever the row changes underneath the screen. */
    reseed(project: Project): void {
        const draft = draftFromProject(project);
        this.sandboxEnabled = draft.sandboxEnabled;
        this.networkIsolation = draft.networkIsolation;
        this.networkIsolationBaseline = draft.networkIsolationBaseline;
        this.memoryLimitMB = draft.memoryLimitMB;
        this.image = draft.image;
        this.dockerfilePath = draft.dockerfilePath;
        this.imageSource = draft.imageSource;
        this.forwardPorts = draft.forwardPorts;
        this.mounts = draft.mounts;
        this.envs = draft.envs;
        this.capabilities = draft.capabilities;
        this.portsExpanded = !!project.forward_ports && project.forward_ports.length > 0;
        this.mountsExpanded = !!project.sandbox_mounts && project.sandbox_mounts.length > 0;
        this.envsExpanded = !!project.sandbox_envs && project.sandbox_envs.length > 0;
        this.capsExpanded = !!project.sandbox_capabilities && project.sandbox_capabilities.length > 0;
        if (this.unreadRow) {
            this.unreadRow = false;
            this.prevSandboxEnabled = draft.sandboxEnabled;
        } else {
            this.autoAnswerIsolation(draft.sandboxEnabled);
        }
    }

    /** The draft as plain data, for the comparison that writes it. */
    snapshot(): SandboxDraft {
        return {
            sandboxEnabled: this.sandboxEnabled,
            networkIsolation: this.networkIsolation,
            networkIsolationBaseline: this.networkIsolationBaseline,
            memoryLimitMB: this.memoryLimitMB,
            image: this.image,
            dockerfilePath: this.dockerfilePath,
            imageSource: this.imageSource,
            forwardPorts: this.forwardPorts,
            mounts: this.mounts,
            envs: this.envs,
            capabilities: this.capabilities,
        };
    }

    /**
     * Turning sandboxing on answers the isolation switch yes, because a sandbox with the network
     * open is not what a person who just enabled isolation asked for — unless they have moved that
     * switch themselves, in which case their answer stands. Call it after every change of the
     * sandbox switch, whoever made the change.
     */
    onSandboxEnabledChanged(): void {
        this.autoAnswerIsolation(this.sandboxEnabled);
    }

    /** The rule, read against the switch's new answer rather than the field that holds it. */
    private autoAnswerIsolation(enabled: boolean): void {
        if (enabled && this.prevSandboxEnabled === false && !this.userToggledNetwork) {
            this.networkIsolation = true;
            this.networkIsolationBaseline = true;
        }
        this.prevSandboxEnabled = enabled;
    }

    /** The isolation row saw a click, so the switch now holds the person's own answer. */
    markNetworkTouched(): void {
        this.userToggledNetwork = true;
    }

    /** The person declined to disable sandboxing: put the row's answer back. */
    restoreSandboxEnabled(project: Project): void {
        this.sandboxEnabled = project.sandbox_enabled ?? false;
        this.onSandboxEnabledChanged();
    }

    /** The Dockerfile this draft picked differs from the one the row names. */
    dockerfileChanged(project: Project): boolean {
        return this.dockerfilePath !== (project.sandbox_dockerfile_path ?? '');
    }

    /** Choose the Dockerfile the project builds its image from. */
    selectDockerfile(path: string): void {
        this.dockerfilePath = path;
        this.imageSource = 'dockerfile';
    }

    chooseImageSource(source: ImageSource): void {
        this.imageSource = source;
        this.buildError = '';
    }

    get hasPendingPort(): boolean { return String(this.newContainerPort).trim() !== ''; }

    get hasPendingMount(): boolean {
        return this.newMountHostPath.trim() !== '' || this.newMountContainerPath.trim() !== '';
    }

    get hasPendingEnv(): boolean { return this.newEnvKey.trim() !== ''; }

    /** Anything typed but not added, which a save would silently drop. */
    get hasPendingRow(): boolean {
        return this.hasPendingPort || this.hasPendingMount || this.hasPendingEnv;
    }

    // ── Forwarded ports ──────────────────────────────────────────────────────

    /**
     * Read the ports the container is publishing now: the project's own terminal plus every running
     * task. Answers with the board's tasks so the caller can count what is in progress, or null when
     * the board could not be read — which is not the answer "nothing is running".
     */
    async loadActivePorts(projectId: string): Promise<Task[] | null> {
        try {
            const proj = await getProject(projectId);
            this.terminalPorts = proj.active_terminal_ports || [];
        } catch {
            // Ignore fetch errors
        }
        try {
            const board = await getBoard(projectId);
            const map = new Map<number, number>();
            for (const ap of this.terminalPorts) {
                map.set(ap.container_port, ap.host_port);
            }
            for (const task of board.tasks) {
                if (task.execution_state === 'running' && task.assigned_ports) {
                    for (const ap of task.assigned_ports) {
                        map.set(ap.container_port, ap.host_port);
                    }
                }
            }
            this.activeHostPorts = map;
            return board.tasks;
        } catch {
            // Ignore fetch errors
            return null;
        }
    }

    editPort(index: number): void {
        this.editingPortIndex = index;
        this.newContainerPort = String(this.forwardPorts[index].container_port);
        this.newHostIP = bindForInput(this.forwardPorts[index].host_ip);
        this.portError = '';
    }

    cancelPortEdit(): void {
        this.editingPortIndex = null;
        this.newContainerPort = '';
        this.newHostIP = '';
        this.portError = '';
    }

    addPortForward(): void {
        this.portError = '';
        this.saveWarning = '';
        const message = portRowError(this.newContainerPort, this.newHostIP);
        if (message) {
            this.portError = message;
            return;
        }
        const container = Number(this.newContainerPort);
        const hostIP = normalizeBind(this.newHostIP) ?? '';
        const entry: PortForward = hostIP ? { container_port: container, host_ip: hostIP } : { container_port: container };
        if (this.editingPortIndex !== null) {
            const updated = [...this.forwardPorts];
            updated[this.editingPortIndex] = entry;
            this.forwardPorts = updated;
            this.editingPortIndex = null;
        } else {
            this.forwardPorts = [...this.forwardPorts, entry];
        }
        this.newContainerPort = '';
        this.newHostIP = '';
    }

    removePortForward(index: number): void {
        this.forwardPorts = this.forwardPorts.filter((_, i) => i !== index);
    }

    // ── Bind mounts ──────────────────────────────────────────────────────────

    editMount(index: number): void {
        this.editingMountIndex = index;
        this.newMountHostPath = this.mounts[index].host_path;
        this.newMountContainerPath = this.mounts[index].container_path;
        this.newMountMode = this.mounts[index].mode;
        this.mountError = '';
    }

    cancelMountEdit(): void {
        this.editingMountIndex = null;
        this.newMountHostPath = '';
        this.newMountContainerPath = '';
        this.newMountMode = 'rw';
        this.mountError = '';
    }

    addMount(): void {
        this.mountError = '';
        this.saveWarning = '';
        if (!this.newMountHostPath.trim()) {
            this.mountError = t('sandbox.mountHostPathError');
            return;
        }
        if (!this.newMountContainerPath.trim()) {
            this.mountError = t('sandbox.mountContainerPathError');
            return;
        }
        const mount = {
            host_path: this.newMountHostPath.trim(),
            container_path: this.newMountContainerPath.trim(),
            mode: this.newMountMode,
        };
        if (this.editingMountIndex !== null) {
            const updated = [...this.mounts];
            updated[this.editingMountIndex] = mount;
            this.mounts = updated;
            this.editingMountIndex = null;
        } else {
            this.mounts = [...this.mounts, mount];
        }
        this.newMountHostPath = '';
        this.newMountContainerPath = '';
        this.newMountMode = 'rw';
    }

    removeMount(index: number): void {
        this.mounts = this.mounts.filter((_, i) => i !== index);
    }

    // ── Environment variables ────────────────────────────────────────────────

    editEnv(index: number): void {
        this.editingEnvIndex = index;
        this.newEnvKey = this.envs[index].key;
        this.newEnvValue = this.envs[index].value;
        this.envError = '';
    }

    cancelEnvEdit(): void {
        this.editingEnvIndex = null;
        this.newEnvKey = '';
        this.newEnvValue = '';
        this.envError = '';
    }

    /**
     * A declaration is always a `KEY=VALUE` pair: the container is given only what was configured,
     * so a name with no value is refused here rather than becoming a bare `-e NAME` that inherits
     * whatever the server happens to hold.
     */
    addEnv(): void {
        this.envError = '';
        this.saveWarning = '';
        if (!this.newEnvKey.trim()) {
            this.envError = t('sandbox.envNameError');
            return;
        }
        const isDuplicate = this.envs.some((e, i) =>
            i !== this.editingEnvIndex && e.key === this.newEnvKey.trim()
        );
        if (isDuplicate) {
            this.envError = t('sandbox.envDuplicateError', { name: this.newEnvKey.trim() });
            return;
        }
        const env = {
            key: this.newEnvKey.trim(),
            value: this.newEnvValue,
        };
        if (this.editingEnvIndex !== null) {
            const updated = [...this.envs];
            updated[this.editingEnvIndex] = env;
            this.envs = updated;
            this.editingEnvIndex = null;
        } else {
            this.envs = [...this.envs, env];
        }
        this.newEnvKey = '';
        this.newEnvValue = '';
    }

    removeEnv(index: number): void {
        this.envs = this.envs.filter((_, i) => i !== index);
    }

    // ── Capabilities ─────────────────────────────────────────────────────────

    capSuggestions = $derived.by(() => capSuggestionsFor(this.capInputValue, this.capabilities));

    addCapability(cap: string): void {
        const normalized = normalizeCapability(cap);
        if (!normalized || this.capabilities.includes(normalized)) return;
        this.capabilities = [...this.capabilities, normalized];
        this.capInputValue = '';
        this.capDropdownOpen = false;
        this.capSelectedIndex = -1;
    }

    removeCapability(cap: string): void {
        this.capabilities = this.capabilities.filter(c => c !== cap);
    }

    /** Tab and the arrows walk the suggestions into the field; Enter adds what the field holds. */
    handleCapInputKeydown(e: KeyboardEvent): void {
        const suggestions = this.capSuggestions;
        if (e.key === 'Tab') {
            e.preventDefault();
            if (suggestions.length > 0) {
                const shift = e.shiftKey;
                if (this.capSelectedIndex < 0) {
                    this.capSelectedIndex = shift ? suggestions.length - 1 : 0;
                } else {
                    this.capSelectedIndex = shift
                        ? (this.capSelectedIndex - 1 + suggestions.length) % suggestions.length
                        : (this.capSelectedIndex + 1) % suggestions.length;
                }
                this.capInputValue = suggestions[this.capSelectedIndex];
            }
        } else if (e.key === 'Enter' && this.capInputValue.trim()) {
            e.preventDefault();
            if (this.capSelectedIndex >= 0 && suggestions[this.capSelectedIndex]) {
                this.addCapability(suggestions[this.capSelectedIndex]);
            } else {
                this.addCapability(this.capInputValue);
            }
        } else if (e.key === 'ArrowDown' && suggestions.length > 0) {
            e.preventDefault();
            this.capSelectedIndex = this.capSelectedIndex < 0 ? 0 : (this.capSelectedIndex + 1) % suggestions.length;
            this.capInputValue = suggestions[this.capSelectedIndex];
        } else if (e.key === 'ArrowUp' && suggestions.length > 0) {
            e.preventDefault();
            this.capSelectedIndex = this.capSelectedIndex < 0
                ? suggestions.length - 1
                : (this.capSelectedIndex - 1 + suggestions.length) % suggestions.length;
            this.capInputValue = suggestions[this.capSelectedIndex];
        } else if (e.key === 'Backspace' && !this.capInputValue && this.capabilities.length > 0) {
            this.removeCapability(this.capabilities[this.capabilities.length - 1]);
        } else if (e.key === 'Escape') {
            this.capDropdownOpen = false;
            this.capSelectedIndex = -1;
        }
    }

    handleCapInputChange(): void {
        this.capSelectedIndex = -1;
        this.capDropdownOpen = true;
    }

    // ── The image build ──────────────────────────────────────────────────────

    async rebuild(project: Project): Promise<void> {
        if (!this.dockerfilePath || this.building) return;
        this.building = true;
        this.buildError = '';
        this.buildLog = [];

        try {
            if (this.dockerfileChanged(project)) {
                await updateProject(project.id, { sandbox_dockerfile_path: this.dockerfilePath });
            }
            await buildSandboxImageStream(project.id, (line: string) => {
                if (line.startsWith('error: ')) {
                    this.buildError = line.slice(7);
                } else {
                    this.buildLog.push(line);
                }
            }, { noCache: this.buildNoCache });
        } catch (e) {
            this.buildError = String(e);
        } finally {
            this.building = false;
        }
    }
}
