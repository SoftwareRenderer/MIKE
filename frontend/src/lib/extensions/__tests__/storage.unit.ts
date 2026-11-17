import { describe, it, expect } from 'vitest';
import { buildStorageNamespace } from '../handlers/storage';

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
