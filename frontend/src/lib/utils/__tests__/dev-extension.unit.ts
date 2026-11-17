import { describe, expect, it } from 'vitest';
import { devRowForProject } from '../dev-extension';
import type { Extension } from '$lib/types';

function row(over: Partial<Extension>): Extension {
    return {
        id: 'dev-1',
        name: 'My Ext',
        version: '0.1.0',
        type: 'ui',
        manifest: {},
        config: {},
        container_config: {},
        sandbox_config: {},
        install_source: 'dev',
        enabled: true,
        running: false,
        installed_at: '',
        built_at: '',
        ...over,
    } as Extension;
}

describe('devRowForProject', () => {
    it('offers no action when the project has no development row', () => {
        expect(devRowForProject([row({ project_id: 'other' })], 'mine')).toBeNull();
        expect(devRowForProject([], 'mine')).toBeNull();
        expect(devRowForProject([row({ project_id: 'mine' })], '')).toBeNull();
    });

    it('offers no action for another project\'s row', () => {
        expect(devRowForProject([row({ project_id: 'not-mine' })], 'mine')).toBeNull();
    });

    it('offers the row of the task\'s own project, loaded or not', () => {
        const unloaded = row({ project_id: 'mine', loaded: false, task_id: undefined });
        expect(devRowForProject([unloaded], 'mine')).toBe(unloaded);
        const loaded = row({ project_id: 'mine', loaded: true, task_id: 'task-1' });
        expect(devRowForProject([loaded], 'mine')).toBe(loaded);
    });

    it('never treats an installed extension as an authoring row', () => {
        const installed = row({ project_id: 'mine', install_source: 'local' });
        expect(devRowForProject([installed], 'mine')).toBeNull();
    });
});
