import { describe, it, expect } from 'vitest';
import { computeGrants, SUPPORTED_CAPABILITIES } from '../capabilities';

/**
 * Least-privilege grant computation: an extension may only ever be granted a
 * capability it both (a) declared in its manifest `permissions` AND (b) that
 * exists in the host catalog. Anything else is dropped — it is unreachable.
 */
describe('computeGrants (manifest permissions ∩ host catalog)', () => {
    it('grants declared permissions that the host supports', () => {
        const granted = computeGrants(['config.get', 'network.proxy']);
        expect(granted).toEqual(['config.get', 'network.proxy']);
    });

    it('drops declared permissions that are NOT in the host catalog', () => {
        const granted = computeGrants(['config.get', 'files.read', 'shell.exec']);
        expect(granted).toEqual(['config.get']);
    });

    it('returns an empty set for missing or non-array permissions', () => {
        expect(computeGrants(undefined)).toEqual([]);
        expect(computeGrants(null as any)).toEqual([]);
        expect(computeGrants('config.get' as any)).toEqual([]);
        expect(computeGrants([])).toEqual([]);
    });

    it('deduplicates repeated declared permissions', () => {
        const granted = computeGrants(['config.get', 'config.get', 'network.proxy', 'config.get']);
        expect(granted).toEqual(['config.get', 'network.proxy']);
    });

    it('grants the editor.jumpToLine event when declared', () => {
        // The event the host forwards to a WYSIWYG extension so it can scroll to a symbol-outline
        // reference: an event with nothing to grant on, so the catalog entry is only its kind.
        expect(SUPPORTED_CAPABILITIES['editor.jumpToLine']).toEqual({ kind: 'event' });
        expect(computeGrants(['editor.jumpToLine'])).toEqual(['editor.jumpToLine']);
    });
});
