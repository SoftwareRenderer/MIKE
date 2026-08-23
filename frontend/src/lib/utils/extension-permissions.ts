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
 * True when an install warrants a pre-install review dialog: the entry
 * declares the `network` capability, or at least one sandbox-escaping
 * permission.
 */
export function requiresInstallReview(entry: { capabilities?: Record<string, any> | null }): boolean {
    return hasNetworkCapability(entry.capabilities) || hasDangerousPermissions((entry.capabilities?.ui?.permissions ?? []) as string[]);
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
