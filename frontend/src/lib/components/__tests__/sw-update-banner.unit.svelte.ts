import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));

describe('SWUpdateBanner', () => {
    it('banner visibility is conditional on detector.needsReload', () => {
        const componentPath = resolve(__dirname, '../SWUpdateBanner.svelte');
        const source = readFileSync(componentPath, 'utf-8');

        // The banner should only render when detector.needsReload is true
        expect(source).toContain('{#if detector.needsReload}');

        // The banner should have the correct class
        expect(source).toContain('class="sw-update-bar"');

        // The banner should have proper ARIA attributes
        expect(source).toContain('role="alert"');
        expect(source).toContain('aria-live="polite"');

        // The reload button should call detector.reload
        expect(source).toContain('detector.reload');
    });
});
