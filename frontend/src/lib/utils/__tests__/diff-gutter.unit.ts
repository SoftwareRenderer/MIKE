import { describe, it, expect } from 'vitest';
import { mergeMarkers, computeDiffMarkers } from '../diff-gutter';

describe('mergeMarkers', () => {
    it('returns server markers when no user edits', () => {
        const content = 'line1\nline2\nline3\n';
        const serverMarkers = new Map([[2, ['modified']]]);
        const result = mergeMarkers(serverMarkers, content, content);
        expect(result).toEqual(new Map([[2, ['modified']]]));
    });

    it('returns empty map when no server markers and no edits', () => {
        const content = 'line1\nline2\nline3\n';
        const result = mergeMarkers(null, content, content);
        expect(result.size).toBe(0);
    });

    it('returns delta markers when no server markers', () => {
        const baseline = 'line1\nline2\nline3\n';
        const current = 'line1\nline2-changed\nline3\n';
        const result = mergeMarkers(null, baseline, current);
        expect(result.get(2)).toEqual(['modified']);
    });

    it('merges server markers with user additions', () => {
        // Server says line 2 is modified (vs HEAD)
        // User adds a line at the end
        const baseline = 'line1\nline2\nline3\n';
        const current = 'line1\nline2\nline3\nline4-new\n';
        const serverMarkers = new Map([[2, ['modified']]]);
        const result = mergeMarkers(serverMarkers, baseline, current);
        expect(result.get(2)).toEqual(['modified']);
        expect(result.get(4)).toEqual(['added']);
    });

    it('shifts server markers when user inserts lines at top', () => {
        // Server says line 2 is modified (vs HEAD)
        // User inserts a line at the top, pushing line 2 to line 3
        const baseline = 'line1\nline2\nline3\n';
        const current = 'line0-new\nline1\nline2\nline3\n';
        const serverMarkers = new Map([[2, ['modified']]]);
        const result = mergeMarkers(serverMarkers, baseline, current);
        // Line 2 (baseline) is now line 3 (current)
        expect(result.get(3)).toEqual(['modified']);
        // Line 1 is the new line
        expect(result.get(1)).toEqual(['added']);
    });

    it('removes server markers for deleted baseline lines', () => {
        // Server says line 2 is modified (vs HEAD)
        // User deletes line 2
        const baseline = 'line1\nline2\nline3\n';
        const current = 'line1\nline3\n';
        const serverMarkers = new Map([[2, ['modified']]]);
        const result = mergeMarkers(serverMarkers, baseline, current);
        // Line 2 was deleted — its marker should not appear in current doc
        expect(result.has(2)).toBe(false);
        // Line 2 (now line3 in current) should not have the server marker
        expect(result.get(2)).toBeUndefined();
    });

    it('handles user modification on a line already marked by server', () => {
        // Server says line 2 is modified (vs HEAD)
        // User further modifies line 2
        const baseline = 'line1\nline2-edited\nline3\n';
        const current = 'line1\nline2-edited-again\nline3\n';
        const serverMarkers = new Map([[2, ['modified']]]);
        const result = mergeMarkers(serverMarkers, baseline, current);
        // Line 2 should be marked (server marker shifted + delta marker merged)
        const types = result.get(2);
        expect(types).toContain('modified');
    });

    it('handles completely replaced document', () => {
        const baseline = 'line1\nline2\nline3\n';
        const current = 'completely\ndifferent\n';
        const serverMarkers = new Map([[1, ['added']], [2, ['modified']], [3, ['added']]]);
        const result = mergeMarkers(serverMarkers, baseline, current);
        // All baseline lines were removed, all current lines are new
        // Server markers for baseline lines are dropped (no mapping)
        // Delta markers show all current lines as added/modified
        expect(result.has(1)).toBe(true);
        expect(result.has(2)).toBe(true);
    });
});

describe('computeDiffMarkers', () => {
    it('detects added lines', () => {
        const original = 'line1\nline2\n';
        const current = 'line1\nline2\nline3\n';
        const markers = computeDiffMarkers(original, current);
        expect(markers[3]?.[0]?.type).toBe('added');
    });

    it('detects removed lines', () => {
        const original = 'line1\nline2\nline3\n';
        const current = 'line1\nline3\n';
        const markers = computeDiffMarkers(original, current);
        // Removed marker appears on line before the gap (line 1)
        const line1Markers = markers[1];
        expect(line1Markers?.some(m => m.type === 'removed')).toBe(true);
    });

    it('detects modified lines', () => {
        const original = 'line1\nline2\nline3\n';
        const current = 'line1\nline2-changed\nline3\n';
        const markers = computeDiffMarkers(original, current);
        expect(markers[2]?.[0]?.type).toBe('modified');
    });

    it('returns all context for identical files', () => {
        const content = 'line1\nline2\n';
        const markers = computeDiffMarkers(content, content);
        for (let i = 1; i < markers.length; i++) {
            if (markers[i]) {
                expect(markers[i][0]?.type).toBe('context');
            }
        }
    });
});
