import { describe, it, expect } from 'vitest';
import { safeUrl } from '../safe-url';

describe('safeUrl — the scheme guard behind every href on the Extensions screen', () => {
    it('passes an http(s) address through untouched', () => {
        expect(safeUrl('https://example.com/license')).toBe('https://example.com/license');
        expect(safeUrl('http://example.com')).toBe('http://example.com');
    });

    it('refuses a scheme the browser would execute or read locally', () => {
        for (const url of [
            'javascript:alert(1)',
            'data:text/html,<script>alert(1)</script>',
            'file:///etc/passwd',
            'vbscript:msgbox(1)',
        ]) {
            expect(safeUrl(url)).toBe('#');
        }
    });

    it('refuses what is not an absolute URL at all', () => {
        expect(safeUrl('')).toBe('#');
        expect(safeUrl('/extensions/some/thing')).toBe('#');
        expect(safeUrl('not a url')).toBe('#');
    });
});
