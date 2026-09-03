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
 * True when installing this extension is only the first half of making it work. Today that is one
 * thing: it declared a dependency directory, which installing does not fill. Add further reasons
 * here rather than adding another check where the card decides to badge.
 */
export function requiresSetupAfterInstall(capabilities?: Record<string, any> | null): boolean {
    return dependencyCacheDeclaration(capabilities) !== null;
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
 * True when this extension declared a dependency directory and nobody has named one yet — the state
 * behind the installed card's yellow dot and its "Needs setup" badge. Without it the server starts
 * and resolves only what its own image contains, which for anything but a stdlib-only project means
 * it answers about imports it could not load.
 *
 * This is the state of an *installed* extension, one with a stored record that can be missing a
 * grant. A Browse entry has no such record yet, so its card uses `requiresSetupAfterInstall`.
 *
 * Disabled extensions are excluded deliberately: one you switched off is not waiting on you, and a
 * warning about a thing that cannot run teaches people to ignore warnings. It reappears on enable.
 *
 * Today this is the only state worth warning about. "Granted, but it still cannot resolve" — the empty
 * directory, the one deleted afterwards — needs the environment verdict wired first (TODO task 15), and
 * will arrive as a second severity here rather than as a louder version of this one.
 */
export function needsDependencyGrant(ext: DependencyGrantView): boolean {
    return !!ext.enabled && !!ext.sandbox_config?.dependency_cache && !(ext.sandbox_config?.dependency_host_path || '').trim();
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
