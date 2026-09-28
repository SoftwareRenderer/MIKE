/**
 * Every capability the host offers is accounted for in the consent UI.
 *
 * The dialog's lists are hand-written copies of `SUPPORTED_CAPABILITIES`, so without this file a
 * new capability would be granted and pushed to while the install dialog showed nothing about it.
 */
import { describe, it, expect } from 'vitest';
import { SUPPORTED_CAPABILITIES } from '../capabilities';
import {
    DANGEROUS_PERMISSIONS,
    permissionDescriptions,
} from '$lib/utils/extension-permissions';

/**
 * Capabilities the host grants itself or pairs with a declared one, so no manifest declares them and
 * the install dialog never lists them. Every catalog capability is either here or described by
 * `permissionDescriptions()`, because anything else is one the user approves with no explanation and
 * no warning. Nothing but this file reads the list — granting is `computeGrants`'s against the
 * catalog — so an entry that stops naming a real capability is caught here and nowhere else.
 */
const HOST_INTERNAL_CAPABILITIES: ReadonlySet<string> = new Set([
    'config.get',
    'log.write',
    'ui.status',
    'theme.get',
    'theme.update',
    'editor.removeFooterButton',
    'editor.footerButtonClick',
    'editor.contentChange',
    'preview.serve',
    'preview.release',
    'preview.navState',
]);

/**
 * Pushed by the host and deliberately not grantable, so it has no catalog entry: the click of a
 * footer button is intrinsic to `editor.addFooterButton` and carries only an id the extension gave.
 */
const PUSHED_ONLY_CAPABILITIES = ['editor.footerButtonClick'];

const catalog = () => Object.keys(SUPPORTED_CAPABILITIES).sort();

describe('capability consent catalog', () => {
    it('describes or dismisses every capability the host can grant', () => {
        const described = Object.keys(permissionDescriptions());
        const unclassified = catalog().filter(
            (cap) => !described.includes(cap) && !HOST_INTERNAL_CAPABILITIES.has(cap)
        );
        expect(unclassified, `classify in permissionDescriptions() or HOST_INTERNAL_CAPABILITIES: ${unclassified.join(', ')}`).toEqual([]);
    });

    it('never both describes a capability and calls it host-internal', () => {
        const both = Object.keys(permissionDescriptions()).filter((cap) => HOST_INTERNAL_CAPABILITIES.has(cap));
        expect(both).toEqual([]);
    });

    it('describes no capability the host does not offer', () => {
        const stale = Object.keys(permissionDescriptions()).filter((cap) => !SUPPORTED_CAPABILITIES[cap]);
        expect(stale, `no longer in the catalog: ${stale.join(', ')}`).toEqual([]);
    });

    it('lists no host-internal capability that is neither in the catalog nor a pushed event', () => {
        const unknown = [...HOST_INTERNAL_CAPABILITIES].filter(
            (cap) => !SUPPORTED_CAPABILITIES[cap] && !PUSHED_ONLY_CAPABILITIES.includes(cap)
        );
        expect(unknown, `not a capability: ${unknown.join(', ')}`).toEqual([]);
    });

    it('flags as dangerous only capabilities the host offers', () => {
        const stale = [...DANGEROUS_PERMISSIONS].filter((cap) => !SUPPORTED_CAPABILITIES[cap]);
        expect(stale, `no longer in the catalog: ${stale.join(', ')}`).toEqual([]);
    });

    it('warns about every capability that leaves the sandbox at all', () => {
        // Names the boundary in both directions: what leaves the sandbox is flagged, and what
        // stops doing so should stop being flagged. `preview.serve` is strict CSP with no egress.
        expect([...DANGEROUS_PERMISSIONS].sort()).toEqual([
            'network.proxy',
            'preview.serveNetwork',
            'preview.serveScripts',
        ]);
    });
});
