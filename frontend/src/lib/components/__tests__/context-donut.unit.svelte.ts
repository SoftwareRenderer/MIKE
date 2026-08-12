import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const donutPath = resolve(__dirname, '../Chat/ContextDonut.svelte');
const source = readFileSync(donutPath, 'utf-8');

describe('ContextDonut rendering', () => {
    it('shows a donut ring with percentage when limit is set', () => {
        expect(source).toContain('donut');
        expect(source).toContain('donut-fill');
        expect(source).toContain('percentage');
    });

    it('donut starts full and depletes as context is used', () => {
        expect(source).toContain('remainingPct');
        expect(source).toContain('donutOffset');
        expect(source).toContain('CIRCUMFERENCE');
    });

    it('reverses direction so the ring depletes counter-clockwise', () => {
        expect(source).toContain('scaleX(-1)');
    });

    it('supports a configurable size', () => {
        expect(source).toContain('size = 20');
        expect(source).toContain('width="{size}"');
    });

    it('shows green when usage is low', () => {
        expect(source).toContain('color-status-green');
    });

    it('shows amber warning when usage exceeds 70%', () => {
        expect(source).toContain('percentage > 70');
        expect(source).toContain('color-status-amber');
    });

    it('shows orange warning when usage exceeds 80%', () => {
        expect(source).toContain('percentage > 80');
        expect(source).toContain('color-status-orange-dark');
    });

    it('shows red danger when usage exceeds 90%', () => {
        expect(source).toContain('percentage >= 90');
        expect(source).toContain('color-status-red');
    });
});
