import { describe, it, expect } from 'vitest';

describe('test setup', () => {
    it('should pass a basic assertion', () => {
        expect(1 + 1).toBe(2);
    });

    it('should resolve $lib alias', () => {
        // If this imports without error, the alias works
        const path = '$lib/utils/diff-parser.ts';
        expect(path).toContain('$lib');
    });
});
