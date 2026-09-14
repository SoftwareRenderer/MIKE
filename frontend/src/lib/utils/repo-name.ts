/**
 * Derive a display name for a git clone target from the source URL —
 * mirrors the backend `repoNameFromGitURL` (backend/api/project.go)
 * so the sidebar placeholder matches the name the server will assign.
 *
 * Supports HTTPS (`https://host/org/repo.git`), SSH
 * (`git@host:org/repo.git`), file URLs and plain local paths.
 */
export function repoNameFromGitUrl(gitUrl: string): string {
    let s = gitUrl.trim();
    // SSH form uses ':' as the separator after the host (no scheme present).
    if (s.includes(':') && !s.includes('://')) {
        s = s.slice(s.lastIndexOf(':') + 1);
    }
    if (s.endsWith('.git')) {
        s = s.slice(0, -'.git'.length);
    }
    if (s.endsWith('/')) {
        s = s.slice(0, -1);
    }
    const base = s.split('/').pop() ?? '';
    if (!base || base === '.' || base === '/') {
        return 'repository';
    }
    return base;
}
