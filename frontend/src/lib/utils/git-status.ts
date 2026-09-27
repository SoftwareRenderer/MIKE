/**
 * The vocabulary of git's one-letter status codes (`M`, `A`, `D`, `?`, `R`, `U`, `C`).
 *
 * Three surfaces show the same letter — a badge in the file tree, a badge in the changes panel, a badge
 * on a diff file — so the letter's meaning lives here once: its word (`gitStatusLabel`), its class
 * (`gitStatusClass`) and its colour (`gitStatusColor`). All three answer for the same alphabet, and
 * `__tests__/git-status.unit.ts` fails when one map learns a letter the others do not.
 */

import { t } from '$lib/i18n';

/**
 * Human-readable name for a status letter.
 *
 * It answers in the active language, because it is asked in two places and only one of
 * them is a bare tooltip: the changes panel passes it to a `title`, and BranchFileBrowser
 * passes it as the status word *inside* a translated `aria-label`
 * (`explorer.branchFiles.gitStatusAria`) — where an English word in a Japanese sentence is
 * the half-translation this file exists to avoid. English renders byte-identical either way.
 */
export function gitStatusLabel(char: string): string {
    switch (char) {
        case 'M': return t('git.status.modified');
        case 'A': return t('git.status.added');
        case 'D': return t('git.status.deleted');
        case '?': return t('git.status.untracked');
        case 'R': return t('git.status.renamed');
        case 'U': return t('git.status.unmerged');
        case 'C': return t('git.status.copied');
        default: return char;
    }
}

/** CSS class that colors a status badge, or '' for a letter we don't style. */
export function gitStatusClass(char: string): string {
    switch (char) {
        case 'M': return 'git-status-modified';
        case 'A': return 'git-status-added';
        case 'D': return 'git-status-deleted';
        case '?': return 'git-status-untracked';
        case 'R': return 'git-status-renamed';
        case 'U': return 'git-status-unmerged';
        case 'C': return 'git-status-copied';
        default: return '';
    }
}

/**
 * The colour a status letter is painted with, as a CSS value — a `var()` reference rather than a
 * resolved colour, so a badge written from JS and a tree row styled by `gitStatusClass` follow the same
 * `--color-git-*` token in `app.css` when a theme retokenizes it.
 */
export function gitStatusColor(char: string): string {
    switch (char) {
        case 'M': return 'var(--color-git-modified)';
        case 'A': return 'var(--color-git-added)';
        case 'D': return 'var(--color-git-deleted)';
        case 'R': return 'var(--color-git-renamed)';
        case 'U': return 'var(--color-git-unmerged)';
        case 'C': return 'var(--color-git-copied)';
        // A letter we do not style is a quiet letter: untracked is the palette's de-emphasized one.
        default: return 'var(--color-git-untracked)';
    }
}

/**
 * Whether discarding a path with this letter deletes the file instead of restoring it. `D` is not one of
 * these letters: a deleted path is in HEAD, so it can be restored.
 */
export function gitStatusAbsentFromHead(char: string): boolean {
    return char === '?' || char === 'A' || char === 'C' || char === 'R';
}

/**
 * Which letter wins when one path carries several statuses (higher = more significant).
 * A conflict outranks a deletion, which outranks a rename, and so on down to untracked.
 */
export function gitStatusPriority(char: string): number {
    switch (char) {
        case 'U': return 7;  // unmerged (conflict)
        case 'D': return 6;  // deleted
        case 'R': return 5;  // renamed
        case 'M': return 4;  // modified
        case 'C': return 3;  // copied
        case 'A': return 2;  // added
        case '?': return 1;  // untracked
        default: return 0;
    }
}
