import { describe, it, expect } from 'vitest';
import {
    DANGEROUS_PERMISSIONS,
    isDangerousPermission,
    hasDangerousPermissions,
    sortPermissions,
    hasNetworkCapability,
    requiresInstallReview,
} from '../extension-permissions';

describe('isDangerousPermission (sandbox-escaping permissions only)', () => {
    it('flags the sandbox-escaping permissions', () => {
        for (const perm of ['network.proxy', 'preview.serveScripts', 'preview.serveNetwork']) {
            expect(isDangerousPermission(perm)).toBe(true);
        }
        expect(DANGEROUS_PERMISSIONS.size).toBe(3);
    });

    it('does not flag sandbox-contained permissions', () => {
        for (const perm of [
            'editor.getContent',
            'editor.setContent',
            'editor.save',
            'editor.setOverlay',
            'editor.contentChange',
            'preview.serve',
            'preview.release',
            'ui.status',
            'ui.setSize',
            'ui.modal',
            'createTask.events.paste',
            'storage.get',
        ]) {
            expect(isDangerousPermission(perm)).toBe(false);
        }
    });

    it('does not flag unknown permission names', () => {
        expect(isDangerousPermission('files.read')).toBe(false);
        expect(isDangerousPermission('')).toBe(false);
    });
});

describe('hasDangerousPermissions', () => {
    it('is true when at least one permission is sandbox-escaping', () => {
        expect(hasDangerousPermissions(['editor.save', 'network.proxy'])).toBe(true);
        expect(hasDangerousPermissions(['preview.serveScripts'])).toBe(true);
    });

    it('is false for lists without dangerous permissions', () => {
        expect(hasDangerousPermissions(['editor.save', 'storage.get', 'ui.setSize'])).toBe(false);
    });

    it('is false for an empty list', () => {
        expect(hasDangerousPermissions([])).toBe(false);
    });

    it('is false for unknown permission names', () => {
        expect(hasDangerousPermissions(['files.read', ''])).toBe(false);
    });
});

describe('sortPermissions (stable: dangerous first, manifest order kept)', () => {
    it('moves dangerous permissions to the top, keeping relative order in both groups', () => {
        const perms = [
            'editor.addFooterButton',
            'preview.serveNetwork',
            'editor.setOverlay',
            'network.proxy',
            'preview.serveScripts',
            'editor.getContent',
        ];
        expect(sortPermissions(perms)).toEqual([
            'preview.serveNetwork',
            'network.proxy',
            'preview.serveScripts',
            'editor.addFooterButton',
            'editor.setOverlay',
            'editor.getContent',
        ]);
    });

    it('returns a permutation of the input — nothing added, dropped, or duplicated', () => {
        const perms = ['network.proxy', 'editor.save', 'preview.serveScripts'];
        const sorted = sortPermissions(perms);
        expect(sorted).toHaveLength(perms.length);
        expect([...sorted].sort()).toEqual([...perms].sort());
    });

    it('leaves a list without dangerous permissions untouched', () => {
        const perms = ['editor.addFooterButton', 'editor.getContent', 'ui.status'];
        expect(sortPermissions(perms)).toEqual(perms);
    });

    it('leaves an all-dangerous list in its original order', () => {
        const perms = ['preview.serveNetwork', 'network.proxy'];
        expect(sortPermissions(perms)).toEqual(perms);
    });

    it('returns an empty list for empty input', () => {
        expect(sortPermissions([])).toEqual([]);
    });

    it('does not mutate the input array', () => {
        const perms = ['editor.save', 'network.proxy'];
        sortPermissions(perms);
        expect(perms).toEqual(['editor.save', 'network.proxy']);
    });
});

describe('hasNetworkCapability (manifest-level network capability)', () => {
    it('is true when a network capability object is declared', () => {
        expect(hasNetworkCapability({ network: { allowed_domains: ['api.github.com'] } })).toBe(true);
    });

    it('is true for an empty network capability object (declared = dangerous)', () => {
        expect(hasNetworkCapability({ network: {} })).toBe(true);
    });

    it('is false without a network capability', () => {
        expect(hasNetworkCapability({ ui: { permissions: [] } })).toBe(false);
        expect(hasNetworkCapability({})).toBe(false);
    });

    it('is false for missing, null, or non-object capabilities / network values', () => {
        expect(hasNetworkCapability(undefined)).toBe(false);
        expect(hasNetworkCapability(null)).toBe(false);
        expect(hasNetworkCapability({ network: null })).toBe(false);
    });
});

describe('requiresInstallReview (network capability OR dangerous permissions)', () => {
    it('is true for a network capability with no UI permissions', () => {
        expect(requiresInstallReview({ capabilities: { network: { allowed_domains: ['x.dev'] } } })).toBe(true);
    });

    it('is true for dangerous UI permissions without a network capability', () => {
        expect(requiresInstallReview({ capabilities: { ui: { permissions: ['network.proxy'] } } })).toBe(true);
    });

    it('is true when both are declared', () => {
        expect(
            requiresInstallReview({
                capabilities: { ui: { permissions: ['network.proxy'] }, network: { allowed_domains: ['x.dev'] } },
            }),
        ).toBe(true);
    });

    it('is false for safe permissions only', () => {
        expect(requiresInstallReview({ capabilities: { ui: { permissions: ['editor.save', 'storage.get'] } } })).toBe(false);
    });

    it('is false for missing or empty capabilities', () => {
        expect(requiresInstallReview({ capabilities: undefined })).toBe(false);
        expect(requiresInstallReview({})).toBe(false);
        expect(requiresInstallReview({ capabilities: {} })).toBe(false);
    });

    it('does not treat a legacy "proxy" key as the network capability', () => {
        expect(requiresInstallReview({ capabilities: { proxy: { allowed_domains: ['x.dev'] } } })).toBe(false);
    });
});
