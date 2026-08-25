import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const source = readFileSync(resolve(__dirname, '../CommitDiffOverlay.svelte'), 'utf-8');

describe('CommitDiffOverlay tab rendering', () => {
    it('sets tab-size 4 on diff line text so tabs render 4 chars wide', () => {
        // The diff row styles must set tab-size: 4 — the browser default is 8,
        // which renders tab characters in diff lines twice as wide as intended.
        const textRule = source.match(/\.diff-row \.diff-text \{[^}]*\}/);
        expect(textRule, '.diff-row .diff-text rule missing').not.toBeNull();
        expect(textRule![0]).toContain('tab-size: 4');

        const contentRule = source.match(/\.diff-row \.diff-line-content \{[^}]*\}/);
        expect(contentRule, '.diff-row .diff-line-content rule missing').not.toBeNull();
        expect(contentRule![0]).toContain('tab-size: 4');
    });
});
