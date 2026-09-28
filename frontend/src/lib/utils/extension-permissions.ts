/**
 * Permission display helpers for Settings -> Extensions. "Dangerous" is a permission that
 * escapes the sandbox or loosens its isolation (external requests, running a page's
 * scripts, page network egress) or the manifest-level `network` capability. Dangerous
 * items get a warning icon and sort first. File-mutating permissions
 * (editor.setContent / editor.save) are deliberately not flagged: they are the obvious
 * contract of an editor extension, not an escape.
 *
 * `lib/extensions/__tests__/capability-consent.unit.ts` checks these lists against the capability
 * catalog.
 */
import { t } from '$lib/i18n';

/** @knipignore The catalog cross-check reads this set as a whole, not one permission at a time. */
export const DANGEROUS_PERMISSIONS: ReadonlySet<string> = new Set([
    'network.proxy',
    'preview.serveScripts',
    'preview.serveNetwork',
]);

export function isDangerousPermission(perm: string): boolean {
    return DANGEROUS_PERMISSIONS.has(perm);
}

/**
 * True when a permission list contains at least one sandbox-escaping
 * permission. Used to decide whether an install warrants a permission
 * review before it starts.
 *
 * @knipignore A list's answer is asserted per list; `requiresInstallReview` ORs three conditions.
 */
export function hasDangerousPermissions(permissions: readonly string[]): boolean {
    return permissions.some(isDangerousPermission);
}

/**
 * True when a registry/local entry declares the manifest-level `network`
 * capability (server-side egress to the extension's allowed domains).
 *
 * @knipignore The capability is asserted on the record alone; `requiresInstallReview` ORs it with two others.
 */
export function hasNetworkCapability(capabilities?: Record<string, any> | null): boolean {
    return !!capabilities?.network;
}

/** A declared dependency directory, or null when the declaration is missing or incomplete. */
function readDependencyCache(dep: unknown): { container_path: string; env: string } | null {
    if (!dep || typeof dep !== 'object') return null;
    const { container_path, env } = dep as Record<string, unknown>;
    if (typeof container_path !== 'string' || typeof env !== 'string') return null;
    return { container_path, env };
}

/**
 * The dependency directory this extension reads, or null when it declares none. Read it before the
 * manifest is fetched, so the UI can warn about the missing folder at install time.
 *
 * Two shapes carry it: the registry index projects it onto `lsp` (scripts/gen-registry.py), while
 * `GET /api/extensions/local` returns the manifest as written, with it under `lsp.sandbox`. Reading
 * only the first leaves every local extension unbadged.
 */
export function dependencyCacheDeclaration(
    capabilities?: Record<string, any> | null,
): { container_path: string; env: string } | null {
    const lsp = capabilities?.lsp;
    return readDependencyCache(lsp?.dependency_cache) ?? readDependencyCache(lsp?.sandbox?.dependency_cache);
}

/**
 * True when installing the extension is only half the job: it wants a folder that installing neither fills
 * nor approves. Add new reasons here instead of another check on the card.
 */
export function requiresSetupAfterInstall(capabilities?: Record<string, any> | null): boolean {
    return dependencyCacheDeclaration(capabilities) !== null || agentMountDeclaration(capabilities).length > 0;
}

export type AgentMountProposal = {
    host_path: string;
    container_path: string;
    mode: string;
};

/**
 * Nothing here reaches a container until the user approves it in the extension's mount control.
 */
export function agentMountDeclaration(capabilities?: Record<string, any> | null): AgentMountProposal[] {
    const mounts = capabilities?.agent?.sandbox?.mount;
    if (!Array.isArray(mounts)) return [];
    return mounts.filter((m: unknown): m is AgentMountProposal => {
        const mount = m as Record<string, unknown> | null;
        return !!mount && typeof mount.container_path === 'string' && typeof mount.host_path === 'string';
    });
}

/** The two fields `needsAgentMountGrant` reads, so a test can pass a plain object. */
export type AgentMountView = {
    enabled?: boolean;
    manifest?: { capabilities?: Record<string, any> | null } | null;
    config?: Record<string, any> | null;
};

export function agentMountGrant(ext: AgentMountView): AgentMountProposal[] {
    const granted = ext.config?.sandbox_mount;
    if (!Array.isArray(granted)) return [];
    return granted.filter((m: unknown): m is AgentMountProposal => {
        const mount = m as Record<string, unknown> | null;
        // Both paths are needed: a folder with no container path could not be mounted, and the endpoint
        // refuses it.
        return !!mount
            && typeof mount.host_path === 'string' && !!mount.host_path
            && typeof mount.container_path === 'string' && !!mount.container_path;
    });
}

/**
 * This is the installed card's "Needs setup" badge. An entry from Browse is not installed yet, so it uses
 * `requiresSetupAfterInstall` instead.
 */
export function needsAgentMountGrant(ext: AgentMountView): boolean {
    return !!ext.enabled
        && agentMountDeclaration(ext.manifest?.capabilities).length > 0
        && agentMountGrant(ext).length === 0;
}

/** The two fields `needsDependencyGrant` reads, so a test can hand it one object literal. */
export type DependencyGrantView = {
    enabled?: boolean;
    sandbox_config?: {
        dependency_cache?: { container_path: string; env: string } | null;
        dependency_host_path?: string | null;
    } | null;
};

/**
 * True when an extension declared a dependency directory and nobody has named one yet —
 * the installed card's yellow dot. Without the grant the server resolves only what its
 * own image contains, so it answers about imports it could not load.
 *
 * This is the state of an *installed* extension with a stored record; a Browse entry has
 * no record yet and uses `requiresSetupAfterInstall`. Disabled extensions are excluded —
 * a warning about something that cannot run teaches people to ignore warnings.
 */
export function needsDependencyGrant(ext: DependencyGrantView): boolean {
    return !!ext.enabled && !!ext.sandbox_config?.dependency_cache && !(ext.sandbox_config?.dependency_host_path || '').trim();
}

/**
 * Where to suggest putting a dependency directory: the container path the extension declared,
 * mirrored under the server's own home. Language-blind by construction — it reads nothing but the
 * declaration and one path the server already reports at /api/home, never a probe — and for the
 * common case it lands on the directory the toolchain already uses.
 */
function suggestedDependencyPath(ext: DependencyGrantView, homeDir: string): string {
    const declared = ext.sandbox_config?.dependency_cache;
    return declared && homeDir ? homeDir + declared.container_path : '';
}

/** What the dialog's field opens with: the granted directory, or the suggestion where none is. */
export function dependencyDraft(ext: DependencyGrantView, homeDir: string): string {
    return ext.sandbox_config?.dependency_host_path || suggestedDependencyPath(ext, homeDir);
}

/**
 * One row of the Edit dialog's agent-folder editor. The container path comes from the extension; the
 * host folder and the mode are what the user chooses. `suggestion` is only placeholder text.
 */
export type AgentMountRow = AgentMountProposal & { suggestion: string };

/** The editor's opening rows: one per folder the manifest suggested, plus every approved folder. */
export function agentMountDraft(ext: AgentMountView): AgentMountRow[] {
    const granted = agentMountGrant(ext);
    const rows = agentMountDeclaration(ext.manifest?.capabilities).map((proposal) => {
        const saved = granted.find((g) => g.container_path === proposal.container_path);
        return {
            host_path: saved?.host_path ?? '',
            container_path: proposal.container_path,
            mode: saved?.mode ?? proposal.mode ?? 'rw',
            suggestion: proposal.host_path,
        };
    });
    // The whole list is sent when the user saves, so an approved folder that gets no row here would be
    // removed by that save.
    for (const extra of granted) {
        if (!rows.some((row) => row.container_path === extra.container_path)) {
            rows.push({ host_path: extra.host_path, container_path: extra.container_path, mode: extra.mode || 'ro', suggestion: '' });
        }
    }
    return rows;
}

/** The list to send: a row whose host folder is empty approves nothing. */
export function agentMountUpdate(rows: readonly AgentMountProposal[]): AgentMountProposal[] {
    return rows
        .filter((row) => row.host_path.trim() !== '')
        .map((row) => ({ host_path: row.host_path.trim(), container_path: row.container_path, mode: row.mode }));
}

/**
 * Turns a list of folders into one sorted string, so two lists can be compared in any order.
 * Comparing JSON text is not enough: the server writes a folder's fields in its own order, so text
 * comparison would report a change the user never made.
 */
export function grantFingerprint(mounts: readonly AgentMountProposal[]): string {
    return mounts.map((m) => `${m.container_path}\u0000${m.host_path}\u0000${m.mode}`).sort().join('\n');
}

/**
 * True when an install warrants a pre-install review dialog: the entry declares the `network`
 * capability or at least one sandbox-escaping permission, or it declares a dependency cache and
 * will therefore ask which directory on this machine it may read.
 */
export function requiresInstallReview(entry: { capabilities?: Record<string, any> | null }): boolean {
    return (
        hasNetworkCapability(entry.capabilities) ||
        hasDangerousPermissions((entry.capabilities?.ui?.permissions ?? []) as string[]) ||
        requiresSetupAfterInstall(entry.capabilities)
    );
}

/**
 * Stable partition for a permission list: dangerous permissions first,
 * everything else in its original (manifest declaration) order — within both
 * groups, relative order is preserved.
 */
export function sortPermissions(permissions: readonly string[]): string[] {
    const dangerous = permissions.filter(isDangerousPermission);
    const rest = permissions.filter((p) => !isDangerousPermission(p));
    return [...dangerous, ...rest];
}

/**
 * The label and explanation the install dialog shows for a declared permission. A function, so
 * the wording comes from the active translation pack and not from whichever one loaded first.
 */
export function permissionDescriptions(): Record<string, { label: string; description: string }> {
    return {
        'createTask.description.insertText': {
            label: t('settings.extensions.perm.createTaskDescriptionInsertText.label'),
            description: t('settings.extensions.perm.createTaskDescriptionInsertText.description'),
        },
        'createTask.events.paste': {
            label: t('settings.extensions.perm.createTaskEventsPaste.label'),
            description: t('settings.extensions.perm.createTaskEventsPaste.description'),
        },
        'network.proxy': {
            label: t('settings.extensions.perm.networkProxy.label'),
            description: t('settings.extensions.perm.networkProxy.description'),
        },
        'ui.setSize': {
            label: t('settings.extensions.perm.uiSetSize.label'),
            description: t('settings.extensions.perm.uiSetSize.description'),
        },
        'ui.modal': {
            label: t('settings.extensions.perm.uiModal.label'),
            description: t('settings.extensions.perm.uiModal.description'),
        },
        'storage.get': {
            label: t('settings.extensions.perm.storageGet.label'),
            description: t('settings.extensions.perm.storageGet.description'),
        },
        'storage.set': {
            label: t('settings.extensions.perm.storageSet.label'),
            description: t('settings.extensions.perm.storageSet.description'),
        },
        'storage.delete': {
            label: t('settings.extensions.perm.storageDelete.label'),
            description: t('settings.extensions.perm.storageDelete.description'),
        },
        'storage.list': {
            label: t('settings.extensions.perm.storageList.label'),
            description: t('settings.extensions.perm.storageList.description'),
        },
        'editor.addFooterButton': {
            label: t('settings.extensions.perm.editorAddFooterButton.label'),
            description: t('settings.extensions.perm.editorAddFooterButton.description'),
        },
        'editor.getContent': {
            label: t('settings.extensions.perm.editorGetContent.label'),
            description: t('settings.extensions.perm.editorGetContent.description'),
        },
        'editor.setContent': {
            label: t('settings.extensions.perm.editorSetContent.label'),
            description: t('settings.extensions.perm.editorSetContent.description'),
        },
        'editor.setOverlay': {
            label: t('settings.extensions.perm.editorSetOverlay.label'),
            description: t('settings.extensions.perm.editorSetOverlay.description'),
        },
        'editor.jumpToLine': {
            label: t('settings.extensions.perm.editorJumpToLine.label'),
            description: t('settings.extensions.perm.editorJumpToLine.description'),
        },
        'editor.save': {
            label: t('settings.extensions.perm.editorSave.label'),
            description: t('settings.extensions.perm.editorSave.description'),
        },
        'preview.serveScripts': {
            label: t('settings.extensions.perm.previewServeScripts.label'),
            description: t('settings.extensions.perm.previewServeScripts.description'),
        },
        'preview.serveNetwork': {
            label: t('settings.extensions.perm.previewServeNetwork.label'),
            description: t('settings.extensions.perm.previewServeNetwork.description'),
        },
    };
}
