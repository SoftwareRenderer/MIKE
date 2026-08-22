import { describe, it, expect } from 'vitest';
import { repoNameFromGitUrl } from '../repo-name';

describe('repoNameFromGitUrl', () => {
    it('extracts the name from HTTPS URLs', () => {
        expect(repoNameFromGitUrl('https://github.com/org/repo.git')).toBe('repo');
        expect(repoNameFromGitUrl('https://github.com/org/repo')).toBe('repo');
        expect(repoNameFromGitUrl('https://gitlab.com/group/sub/repo.git')).toBe('repo');
        // Dots in the name are preserved
        expect(repoNameFromGitUrl('https://github.com/org/my.repo.git')).toBe('my.repo');
    });

    it('extracts the name from SSH URLs (host:user/repo form)', () => {
        expect(repoNameFromGitUrl('git@github.com:org/repo.git')).toBe('repo');
        expect(repoNameFromGitUrl('git@github.com:org/repo')).toBe('repo');
        // ssh:// form has a scheme, so it goes through the path branch
        expect(repoNameFromGitUrl('ssh://git@github.com/org/repo.git')).toBe('repo');
    });

    it('extracts the basename from local paths', () => {
        expect(repoNameFromGitUrl('/tmp/kanban-test-repo-2')).toBe('kanban-test-repo-2');
        expect(repoNameFromGitUrl('/tmp/my/local/repo')).toBe('repo');
    });

    it('handles file:// URLs', () => {
        expect(repoNameFromGitUrl('file:///tmp/source')).toBe('source');
    });

    it('trims surrounding whitespace', () => {
        expect(repoNameFromGitUrl('  https://github.com/org/repo.git  ')).toBe('repo');
    });

    it('falls back to a generic name when no basename can be derived', () => {
        expect(repoNameFromGitUrl('/')).toBe('repository');
        expect(repoNameFromGitUrl('.git')).toBe('repository');
    });
});
