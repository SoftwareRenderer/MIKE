/**
 * Permission display helpers for the Settings → Extensions UI.
 *
 * "Dangerous" = a permission that escapes the sandbox or loosens its isolation
 * (external requests, running a page's scripts, page network egress). Dangerous
 * permissions get a warning icon next to their label and sort to the top of the
 * permission lists, so they are the first thing a reviewer sees.
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
 * Stable partition for a permission list: dangerous permissions first,
 * everything else in its original (manifest declaration) order — within both
 * groups, relative order is preserved.
 */
export function sortPermissions(permissions: readonly string[]): string[] {
    const dangerous = permissions.filter(isDangerousPermission);
    const rest = permissions.filter((p) => !isDangerousPermission(p));
    return [...dangerous, ...rest];
}
