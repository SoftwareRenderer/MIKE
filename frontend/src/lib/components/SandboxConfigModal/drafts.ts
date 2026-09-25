import type { PortForward, Project, ProjectUpdates, SandboxEnv, SandboxMount } from '$lib/types';
import { DEFAULT_MEMORY_LIMIT_MB, memoryLimitToInput, parseMemoryLimitMB } from '$lib/utils/memory-stats';

/**
 * What the Sandbox Configuration screen edits before saving: the project's sandbox record with the
 * person's unsaved answers on top, read by the modal that owns the write and by every section that
 * edits part of it. Nothing here is reactive — the reactive holder is `draft.svelte.ts`, and the
 * diff that decides the one `PUT` is this file, so the payload has one definition.
 */

/** Which of the two container images the project builds from. */
export type ImageSource = 'image' | 'dockerfile';

/** The editable half of a project's sandbox record, plus which image source the form shows. */
export interface SandboxDraft {
    /** Where a task runs. Exactly this value decides it — nothing re-checks it later. */
    sandboxEnabled: boolean;
    networkIsolation: boolean;
    /** The number the field holds: null is a blank field, which means no limit. */
    memoryLimitMB: number | null;
    image: string;
    dockerfilePath: string;
    imageSource: ImageSource;
    forwardPorts: PortForward[];
    mounts: SandboxMount[];
    envs: SandboxEnv[];
    capabilities: string[];
    /**
     * What the isolation switch is measured against, not what the row stores: the screen turns
     * isolation on when sandboxing is turned on, and that auto-answer is not an answer of the
     * person's own, so it is not written.
     */
    networkIsolationBaseline: boolean;
}

/** The starting point of a draft: the stored record, with the limit shaped for the field. */
export function draftFromProject(project: Project): SandboxDraft {
    const dockerfilePath = project.sandbox_dockerfile_path ?? '';
    return {
        sandboxEnabled: project.sandbox_enabled ?? false,
        networkIsolation: project.sandbox_network_isolation ?? false,
        memoryLimitMB: memoryLimitToInput(project.sandbox_memory_limit ?? DEFAULT_MEMORY_LIMIT_MB),
        image: project.sandbox_image ?? '',
        dockerfilePath,
        imageSource: dockerfilePath ? 'dockerfile' : 'image',
        forwardPorts: project.forward_ports ?? [],
        mounts: project.sandbox_mounts ?? [],
        envs: project.sandbox_envs ?? [],
        capabilities: project.sandbox_capabilities ?? [],
        networkIsolationBaseline: project.sandbox_network_isolation ?? false,
    };
}

/** The result of comparing a draft with the stored row, plus what the write must ask about first. */
export interface SandboxChanges {
    /** The body of the one `PUT`. Empty means nothing was edited. */
    changes: ProjectUpdates;
    /** Turning sandboxing off is the one edit that needs a person's answer first. */
    needsConfirmation: boolean;
}

function portsChanged(stored: PortForward[], draft: PortForward[]): boolean {
    return draft.length !== stored.length ||
        draft.some((p, i) => p.container_port !== stored[i]?.container_port ||
            (p.host_ip ?? '') !== (stored[i]?.host_ip ?? ''));
}

function mountsChanged(stored: SandboxMount[], draft: SandboxMount[]): boolean {
    return draft.length !== stored.length ||
        draft.some((m, i) => m.host_path !== stored[i]?.host_path ||
            m.container_path !== stored[i]?.container_path ||
            m.mode !== stored[i]?.mode);
}

function envsChanged(stored: SandboxEnv[], draft: SandboxEnv[]): boolean {
    return draft.length !== stored.length ||
        draft.some((e, i) => e.key !== stored[i]?.key || e.value !== stored[i]?.value);
}

function capabilitiesChanged(stored: string[], draft: string[]): boolean {
    return draft.length !== stored.length || draft.some((c, i) => c !== stored[i]);
}

/**
 * The whole configuration is one write: every field that differs from the row is collected here and
 * nothing else issues a request. An unset field is absent, so a section nobody touched is never
 * rewritten — and a limit cleared to blank is sent as an explicit 0, because null tells the server
 * to leave the limit alone.
 */
export function projectChanges(project: Project, draft: SandboxDraft): SandboxChanges {
    const changes: ProjectUpdates = {};
    let needsConfirmation = false;

    if (draft.sandboxEnabled !== (project.sandbox_enabled ?? false)) {
        changes.sandbox_enabled = draft.sandboxEnabled;
        if (!draft.sandboxEnabled) {
            needsConfirmation = true;
        }
    }
    if (draft.networkIsolation !== draft.networkIsolationBaseline) {
        changes.sandbox_network_isolation = draft.networkIsolation;
    }
    const memoryLimitMB = parseMemoryLimitMB(draft.memoryLimitMB);
    if (memoryLimitMB !== parseMemoryLimitMB(project.sandbox_memory_limit ?? DEFAULT_MEMORY_LIMIT_MB)) {
        changes.sandbox_memory_limit = memoryLimitMB;
    }

    // The two image sources are alternatives: switching clears the field the other one used.
    if (draft.imageSource === 'image') {
        if (draft.image !== (project.sandbox_image ?? '')) {
            changes.sandbox_image = draft.image;
        }
        if (project.sandbox_dockerfile_path) {
            changes.sandbox_dockerfile_path = '';
        }
    } else {
        if (draft.dockerfilePath !== (project.sandbox_dockerfile_path ?? '')) {
            changes.sandbox_dockerfile_path = draft.dockerfilePath;
        }
        if (project.sandbox_image) {
            changes.sandbox_image = '';
        }
    }

    if (portsChanged(project.forward_ports ?? [], draft.forwardPorts)) {
        changes.sandbox_forward_ports = draft.forwardPorts;
    }
    if (mountsChanged(project.sandbox_mounts ?? [], draft.mounts)) {
        changes.sandbox_mounts = draft.mounts;
    }
    if (envsChanged(project.sandbox_envs ?? [], draft.envs)) {
        changes.sandbox_envs = draft.envs;
    }
    if (capabilitiesChanged(project.sandbox_capabilities ?? [], draft.capabilities)) {
        changes.sandbox_capabilities = draft.capabilities;
    }

    return { changes, needsConfirmation };
}

/**
 * Mirrors `sandbox.ProjectImageName`: a project's Dockerfile image is a local build, so podman
 * stores it as `localhost/sandbox-<name>`. Keep the two in step.
 */
export function imageTagFor(projectName: string): string {
    return `localhost/sandbox-${projectName.toLowerCase().replace(/ /g, '-').replace(/\//g, '-')}`;
}

/** Files the Dockerfile picker lists and accepts. */
export function isDockerfile(name: string): boolean {
    const lower = name.toLowerCase();
    return lower === 'dockerfile' || lower.endsWith('.dockerfile');
}
