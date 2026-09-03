/**
 * The vocabulary of git's one-letter status codes (`M`, `A`, `D`, `?`, `R`, `U`, `C`).
 *
 * Both file trees surface the same letter — a badge in the tree, a badge in the changes
 * panel — so the letter's *meaning* lives here once. Their colours deliberately do not:
 * the tree colours itself through CSS classes (see `gitStatusClass`), while the changes
 * panel paints inline hex values for its own badge design (see `getStatusBadge` in
 * GitStatusPanel.svelte), and diff-file badges come from `getStatusBadge` in diff-parser.
 */

/** Human-readable name for a status letter, used in `title`/`aria-label` text. */
export function gitStatusLabel(char: string): string {
    switch (char) {
        case 'M': return 'Modified';
        case 'A': return 'Added';
        case 'D': return 'Deleted';
        case '?': return 'Untracked';
        case 'R': return 'Renamed';
        case 'U': return 'Unmerged';
        case 'C': return 'Copied';
        default: return char;
    }
}

/** CSS class that colours a status badge, or '' for a letter we don't style. */
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
