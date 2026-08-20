import { describe, it, expect } from 'vitest';
import {
    DANGEROUS_PERMISSIONS,
    isDangerousPermission,
    hasDangerousPermissions,
    sortPermissions,
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
