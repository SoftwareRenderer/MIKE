import { describe, it, expect } from 'vitest';
import { DOCKER_DEFAULT_CAPABILITIES, KNOWN_CAPABILITIES, capSuggestionsFor, capabilitiesToAdd, normalizeCapability } from '$lib/components/SandboxConfigModal/capabilities';
import { en } from '$lib/i18n/en';

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

/** What a container is really given, so the set is pinned by hand rather than derived. */
describe('the Docker default capability set', () => {
    it("is Docker's default set in Docker's order", () => {
        expect([...DOCKER_DEFAULT_CAPABILITIES]).toEqual([
            'CAP_CHOWN', 'CAP_DAC_OVERRIDE', 'CAP_FSETID', 'CAP_FOWNER', 'CAP_MKNOD', 'CAP_NET_RAW',
            'CAP_SETGID', 'CAP_SETUID', 'CAP_SETFCAP', 'CAP_SETPCAP', 'CAP_NET_BIND_SERVICE',
            'CAP_SYS_CHROOT', 'CAP_KILL', 'CAP_AUDIT_WRITE',
        ]);
    });

    it('grants only names the section offers and describes', () => {
        for (const cap of DOCKER_DEFAULT_CAPABILITIES) {
            expect(KNOWN_CAPABILITIES).toContain(cap);
            expect(typeof en[`sandbox.cap.${cap}`]).toBe('string');
        }
    });

    it("answers the names not already granted, in the set's order", () => {
        expect(capabilitiesToAdd([])).toEqual([...DOCKER_DEFAULT_CAPABILITIES]);
        expect(capabilitiesToAdd(['CAP_KILL', 'CAP_CHOWN'])).toEqual(
            DOCKER_DEFAULT_CAPABILITIES.filter(cap => cap !== 'CAP_KILL' && cap !== 'CAP_CHOWN'),
        );
    });

    it('has nothing to add once every default is granted', () => {
        expect(capabilitiesToAdd([...DOCKER_DEFAULT_CAPABILITIES])).toEqual([]);
    });
});
