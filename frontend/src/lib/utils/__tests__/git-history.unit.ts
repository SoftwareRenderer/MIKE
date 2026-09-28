import { describe, it, expect } from 'vitest';
import {
    getHistoryDensity,
    buildCommitTooltip,
    HISTORY_DENSITY_BREAKPOINTS,
} from '$lib/utils/git-history';
import type { GitCommit } from '$lib/types';

const COMMIT: GitCommit = {
    hash: 'abcdef1234567890abcdef1234567890abcdef12',
    short: 'abcdef1',
    subject: 'Add the thing',
    author: 'Sandbox',
    date: '2024-05-04T14:30:00Z',
    parents: [],
    branches: ['feature/thing'],
};

const CTX = {
    primaryBranch: 'feature/thing',
    isOriginTip: true,
    dateFormatted: '2024-05-04 14:30:00',
};

describe('getHistoryDensity', () => {
    it('returns normal at and above the compact breakpoint', () => {
        expect(getHistoryDensity(1000)).toBe('normal');
        // Default Explorer panel (250px − 1px border = 249px row) stays normal.
        expect(getHistoryDensity(249)).toBe('normal');
        expect(getHistoryDensity(HISTORY_DENSITY_BREAKPOINTS.compact)).toBe('normal');
    });

    it('returns compact between the compact and tight breakpoints', () => {
        expect(getHistoryDensity(244)).toBe('compact');
        expect(getHistoryDensity(205)).toBe('compact');
    });

    it('returns tight between the tight and tighest breakpoints', () => {
        expect(getHistoryDensity(204)).toBe('tight');
        expect(getHistoryDensity(180)).toBe('tight');
    });

    it('returns tighest below the tighest breakpoint', () => {
        // Minimum Explorer panel (180px − 1px border = 179px row) is tighest.
        expect(getHistoryDensity(179)).toBe('tighest');
        expect(getHistoryDensity(100)).toBe('tighest');
    });
});

describe('buildCommitTooltip', () => {
    it('returns undefined for normal density', () => {
        expect(buildCommitTooltip(COMMIT, 'normal', CTX)).toBeUndefined();
    });

    it('includes every field for compact density', () => {
        const tip = buildCommitTooltip(COMMIT, 'compact', CTX);
        expect(tip).toBeDefined();
        const lines = tip!.split('\n');
        expect(lines).toEqual([
            'Add the thing',
            'Sandbox · 2024-05-04 14:30:00',
            'feature/thing · origin',
            'abcdef1',
        ]);
    });

    it('carries the hidden date for tight density', () => {
        const tip = buildCommitTooltip(COMMIT, 'tight', CTX);
        expect(tip).toContain('Sandbox · 2024-05-04 14:30:00');
    });

    it('carries the hidden author for tighest density', () => {
        const tip = buildCommitTooltip(COMMIT, 'tighest', CTX);
        expect(tip).toContain('Sandbox · 2024-05-04 14:30:00');
    });

    it('omits the branch/origin line when the commit has neither', () => {
        const bare: GitCommit = { ...COMMIT, branches: [] };
        const ctx = { primaryBranch: null, isOriginTip: false, dateFormatted: CTX.dateFormatted };
        const tip = buildCommitTooltip(bare, 'compact', ctx);
        expect(tip!.split('\n')).toEqual([
            'Add the thing',
            'Sandbox · 2024-05-04 14:30:00',
            'abcdef1',
        ]);
    });

    it('falls back to the raw date when no formatted date is available', () => {
        const tip = buildCommitTooltip(COMMIT, 'compact', { ...CTX, dateFormatted: '' });
        expect(tip).toContain('Sandbox · 2024-05-04T14:30:00Z');
    });

    it('omits the who/when line when both author and date are empty', () => {
        const empty: GitCommit = { ...COMMIT, author: '', date: '' };
        const tip = buildCommitTooltip(empty, 'tight', { ...CTX, dateFormatted: '' });
        expect(tip!.split('\n')).toEqual([
            'Add the thing',
            'feature/thing · origin',
            'abcdef1',
        ]);
    });
});
