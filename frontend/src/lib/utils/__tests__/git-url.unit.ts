import { describe, it, expect } from 'vitest';
import { gitUrlCarriesCredentials } from '../git-url';

// The queue is persisted to IndexedDB, so a URL queued while offline outlives the
// tab. The rule has to catch the token a user pastes without also refusing every
// private-repo URL — the ssh forms below are how those are normally cloned, and they
// carry no secret.

describe('gitUrlCarriesCredentials', () => {
    it.each([
        ['an https password', 'https://user:ghp_secrettoken@github.com/org/repo.git'],
        ['an https token as the whole userinfo', 'https://ghp_secrettoken@github.com/org/repo.git'],
        ['a percent-encoded token', 'https://user:p%40ss%3Aword@host/org/repo.git'],
        ['a token on plain http', 'http://user:token@host/org/repo.git'],
        ['a password on another transport', 'ssh://user:pw@host:22/org/repo.git'],
        ['userinfo with an at-sign inside', 'https://userpa%40ss@host/repo.git'],
    ])('flags %s', (_name, url) => {
        expect(gitUrlCarriesCredentials(url)).toBe(true);
    });

    it.each([
        ['a plain https clone', 'https://github.com/org/repo.git'],
        ['an empty userinfo', 'https://@host/org/repo.git'],
        ['the scp-like ssh form', 'git@github.com:org/repo.git'],
        ['the scp-like form with a user', 'deploy@build.example.com:/srv/git/repo.git'],
        ['an ssh URL naming ssh’s own user', 'ssh://git@host:22/org/repo.git'],
        ['a file URL', 'file:///srv/git/repo.git'],
        ['a bare local path', '/srv/git/repo.git'],
        ['a relative path', '../repo'],
        ['empty', ''],
        ['whitespace', '   '],
    ])('leaves %s alone', (_name, url) => {
        expect(gitUrlCarriesCredentials(url)).toBe(false);
    });

    it('is not fooled by an @ in the path rather than the userinfo', () => {
        expect(gitUrlCarriesCredentials('https://host/org/repo@main.git')).toBe(false);
    });

    it('is not fooled by a colon in the host port', () => {
        expect(gitUrlCarriesCredentials('https://host:8443/org/repo.git')).toBe(false);
    });

    it('still ignores the scp-like form when it contains a colon and an @', () => {
        // No `://`, so there is no userinfo to read: `git` is the ssh user.
        expect(gitUrlCarriesCredentials('git@gitlab.example.com:2222/team/app.git')).toBe(false);
    });
});
