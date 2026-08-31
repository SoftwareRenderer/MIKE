/**
 * Permission display helpers for the Settings → Extensions UI.
 *
 * "Dangerous" = a permission that escapes the sandbox or loosens its isolation
 * (external requests, running a page's scripts, page network egress), or the
 * manifest-level `network` capability, which lets the server make outbound
 * requests to the extension's allowed domains. Dangerous items get a warning
 * icon and sort to the top of the lists, so they are the first thing a
 * reviewer sees.
 *
 * File-mutating permissions (editor.setContent / editor.save) are deliberately
 * NOT flagged — they are the obvious contract of editor extensions, not a
 * sandbox escape.
 */

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
 */
export function hasDangerousPermissions(permissions: readonly string[]): boolean {
    return permissions.some(isDangerousPermission);
}

/**
 * True when a registry/local entry declares the manifest-level `network`
 * capability (server-side egress to the extension's allowed domains).
 */
export function hasNetworkCapability(capabilities?: Record<string, any> | null): boolean {
    return !!capabilities?.network;
}

/**
 * True when an LSP extension declares a sandbox probe: a command it asks the
 * project's *own* sandbox, to find where that language keeps the code it resolves
 * (a Go module cache, a node_modules root).
 *
 * The probe never runs on the host — it runs in the container the project already
 * runs in, reads one short answer, and can only ever name a directory the user
 * already configured as a mount. It is still a command the extension wrote, so
 * the install review says so in plain words instead of shipping it silently.
 *
 * Reads both shapes: a registry entry carries the `sandbox_probe` marker the
 * index generator derives, an installed extension carries the manifest's own
 * `sandbox.detect` list.
 */
export function hasSandboxProbe(capabilities?: Record<string, any> | null): boolean {
    const lspCaps = capabilities?.lsp;
    if (!lspCaps) return false;
    return lspCaps.sandbox_probe === true || (lspCaps.sandbox?.detect?.length ?? 0) > 0;
}

export const SANDBOX_PROBE_PERMISSION = 'lsp.sandboxProbe';

export const SANDBOX_PROBE_INFO = {
    label: 'Looks for the project’s code packages',
    description:
        'Runs one short command inside this project’s own sandbox to find where the language keeps ' +
        'the packages it already downloaded, so the editor can resolve imports. It never runs on ' +
        'your computer, and it can only use a folder you already shared with the sandbox.',
};

/**
 * True when an install warrants a pre-install review dialog: the entry
 * declares the `network` capability, at least one sandbox-escaping
 * permission, or an LSP sandbox probe.
 */
export function requiresInstallReview(entry: { capabilities?: Record<string, any> | null }): boolean {
    return (
        hasNetworkCapability(entry.capabilities) ||
        hasDangerousPermissions((entry.capabilities?.ui?.permissions ?? []) as string[]) ||
        hasSandboxProbe(entry.capabilities)
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
