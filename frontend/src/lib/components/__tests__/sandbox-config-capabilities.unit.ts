import { describe, it, expect } from 'vitest';
import { KNOWN_CAPABILITIES, capSuggestionsFor, normalizeCapability } from '$lib/components/SandboxConfigModal/capabilities';

/**
 * The capability input's suggestion list. The names themselves are Linux constants and are what a
 * save writes, so the list's shape is the contract: never a name already granted, never more than
 * one dropdown's worth, and never case-sensitive.
 */
describe('sandbox capability suggestions', () => {
    it('offers nothing until something is typed', () => {
        expect(capSuggestionsFor('', [])).toEqual([]);
        expect(capSuggestionsFor('   ', [])).toEqual([]);
    });

    it('matches on any part of the name, whatever case it was typed in', () => {
        expect(capSuggestionsFor('net_raw', [])).toEqual(['CAP_NET_RAW']);
        expect(capSuggestionsFor('cap_sys_tim', [])).toEqual(['CAP_SYS_TIME']);
    });

    it('leaves out what is already granted', () => {
        expect(capSuggestionsFor('NET', ['CAP_NET_RAW'])).not.toContain('CAP_NET_RAW');
        expect(capSuggestionsFor('NET', ['CAP_NET_RAW']).length).toBeGreaterThan(0);
    });

    it('shows at most eight suggestions', () => {
        expect(capSuggestionsFor('CAP', [])).toHaveLength(8);
    });

    it('names every suggestion the way the container names a capability', () => {
        for (const cap of KNOWN_CAPABILITIES) {
            expect(cap).toMatch(/^CAP_[A-Z_]+$/);
        }
    });

    it('reads the typed name the way the grant is written', () => {
        expect(normalizeCapability('  cap_net_raw ')).toBe('CAP_NET_RAW');
        expect(normalizeCapability('   ')).toBe('');
    });
});
