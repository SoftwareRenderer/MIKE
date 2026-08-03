import { describe, it, expect } from 'vitest';
import { buildStorageNamespace, CAP_STORAGE_GET } from '../handlers/storage';
import { computeGrants, SUPPORTED_CAPABILITIES } from '../capabilities';

describe('buildStorageNamespace', () => {
    it('uses project + task scope from the injection context', () => {
        expect(buildStorageNamespace({ projectId: 'proj1', taskId: 'task1' })).toBe('p:proj1/t:task1');
    });

    it('uses only the project scope when no task is present', () => {
        expect(buildStorageNamespace({ projectId: 'proj1' })).toBe('p:proj1');
    });

    it('falls back to global for unscoped contexts', () => {
        expect(buildStorageNamespace({})).toBe('global');
        expect(buildStorageNamespace(undefined)).toBe('global');
        expect(buildStorageNamespace(null)).toBe('global');
    });
});

describe('storage capability catalog', () => {
    it('registers storage.get/set/delete/list as call capabilities', () => {
        for (const cap of ['storage.get', 'storage.set', 'storage.delete', 'storage.list']) {
            const def = SUPPORTED_CAPABILITIES[cap];
            expect(def).toBeTruthy();
            expect(def.kind).toBe('call');
        }
    });

    it('is grantable through computeGrants', () => {
        expect(computeGrants(['storage.get', 'storage.set'])).toEqual(['storage.get', 'storage.set']);
        expect(CAP_STORAGE_GET).toBe('storage.get');
    });
});
