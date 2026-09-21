import { describe, it, expect } from 'vitest';
import { gitStatusLabel, gitStatusClass, gitStatusPriority, gitStatusAbsentFromHead } from '../git-status';

describe('gitStatusLabel', () => {
    it('names every letter git reports for a changed path', () => {
        expect(gitStatusLabel('M')).toBe('Modified');
        expect(gitStatusLabel('A')).toBe('Added');
        expect(gitStatusLabel('D')).toBe('Deleted');
        expect(gitStatusLabel('?')).toBe('Untracked');
        expect(gitStatusLabel('R')).toBe('Renamed');
        expect(gitStatusLabel('U')).toBe('Unmerged');
        expect(gitStatusLabel('C')).toBe('Copied');
    });

    it('falls back to the letter itself for anything else', () => {
        // A status we don't know is still worth reading aloud, not a blank title.
        expect(gitStatusLabel('T')).toBe('T');
        expect(gitStatusLabel('')).toBe('');
    });
});

describe('gitStatusClass', () => {
    it('maps every known letter to its badge class', () => {
        expect(gitStatusClass('M')).toBe('git-status-modified');
        expect(gitStatusClass('A')).toBe('git-status-added');
        expect(gitStatusClass('D')).toBe('git-status-deleted');
        expect(gitStatusClass('?')).toBe('git-status-untracked');
        expect(gitStatusClass('R')).toBe('git-status-renamed');
        expect(gitStatusClass('U')).toBe('git-status-unmerged');
        expect(gitStatusClass('C')).toBe('git-status-copied');
    });

    it('returns no class for an unknown letter so the badge keeps its default color', () => {
        expect(gitStatusClass('T')).toBe('');
    });

    it('names and classes the same set of letters', () => {
        // A letter with a label but no class renders as an unstyled badge, which is how a
        // newly-handled status slips in unnoticed.
        const known = ['M', 'A', 'D', '?', 'R', 'U', 'C'];
        for (const c of known) {
            expect(gitStatusClass(c), c).not.toBe('');
            expect(gitStatusLabel(c), c).not.toBe(c);
        }
    });
});

describe('gitStatusPriority', () => {
    it('ranks a conflict above every other status', () => {
        for (const c of ['D', 'R', 'M', 'C', 'A', '?']) {
            expect(gitStatusPriority('U')).toBeGreaterThan(gitStatusPriority(c));
        }
    });

    it('ranks a deletion above the statuses it can accompany', () => {
        expect(gitStatusPriority('D')).toBeGreaterThan(gitStatusPriority('M'));
        expect(gitStatusPriority('M')).toBeGreaterThan(gitStatusPriority('A'));
    });

    it('ranks nothing for an unknown letter', () => {
        expect(gitStatusPriority('T')).toBe(0);
        expect(gitStatusPriority('')).toBe(0);
    });
});

describe('gitStatusAbsentFromHead', () => {
    it('calls the letters naming a path no commit holds absent from HEAD', () => {
        for (const c of ['?', 'A', 'C', 'R']) {
            expect(gitStatusAbsentFromHead(c)).toBe(true);
        }
    });

    it('keeps a deletion restorable, because HEAD holds the path it removed', () => {
        for (const c of ['D', 'M', 'U']) {
            expect(gitStatusAbsentFromHead(c)).toBe(false);
        }
        expect(gitStatusAbsentFromHead('T')).toBe(false);
    });
});
