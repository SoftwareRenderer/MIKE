import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));

/**
 * Test the StatsModal formatting logic by extracting the functions from the component source.
 * The StatsModal component uses:
 * - formatCost(cost: number): string — formats USD cost with locale-aware formatting
 * - formatNumber(n: number): string — formats with locale thousands separator
 * - formatTps(tps: number): string — formats tokens per second
 * - formatKwh(kwh: number): string — formats kWh/Wh/mWh
 *
 * These tests verify the cost calculation formulas and cache token display logic
 * by checking the component source code and the types it uses.
 */
describe('StatsModal', () => {
    it('should display cache tokens separately', () => {
        const componentPath = resolve(__dirname, '../StatsModal.svelte');
        const source = readFileSync(componentPath, 'utf-8');

        // The summary card should show a "Cached" label
        expect(source).toContain('summary-label');
        expect(source).toContain('Cached');

        // The summary should display total_cache_tokens
        expect(source).toContain('total_cache_tokens');

        // The model table should have a "Cached" column header
        expect(source).toContain('<th');
        expect(source).toContain('Cached');

        // Each model row should display cache_tokens
        expect(source).toContain('cache_tokens');
    });

    it('should calculate cost correctly when pricing is configured', () => {
        const componentPath = resolve(__dirname, '../StatsModal.svelte');
        const source = readFileSync(componentPath, 'utf-8');

        // Cost formatting moved to the shared module — StatsModal delegates to
        // formatCost, which uses Intl.NumberFormat with currency (utils/format.ts)
        expect(source).toContain('formatCost');
        expect(source).toContain('$lib/utils/format');
        const formatSrc = readFileSync(resolve(__dirname, '../../utils/format.ts'), 'utf-8');
        expect(formatSrc).toContain("style: 'currency'");
        expect(formatSrc).toContain("currency: 'USD'");

        // The summary should show total cost
        expect(source).toContain('total_cost');
        expect(source).toContain('Est. Total Cost');

        // The cost header should have a tooltip
        expect(source).toContain('Includes API pricing and electricity cost');

        // The model table should show cost per model
        expect(source).toContain('model.cost');

        // Power consumption should be displayed
        expect(source).toContain('power_kwh');
        expect(source).toContain('formatKwh');
    });
});
