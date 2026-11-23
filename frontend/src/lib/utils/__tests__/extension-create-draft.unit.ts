import { describe, expect, it } from 'vitest';
import { DOCKER_DEFAULT_CAPABILITIES } from '$lib/components/SandboxConfigModal/capabilities';
import { extensionCreateSandboxOptions, validateExtensionCreateDraft } from '../extension-create-draft';

const submittable = (name: string, description: string) =>
    Object.keys(validateExtensionCreateDraft({ name, description })).length === 0;

describe('extension create draft', () => {
    it('refuses a click that could only have been a refusal', () => {
        expect(validateExtensionCreateDraft({ name: '  ', description: 'does a thing' }))
            .toEqual({ name: 'settings.extensions.createNameRequired' });
        expect(validateExtensionCreateDraft({ name: 'Clock', description: '   ' }))
            .toEqual({ description: 'settings.extensions.createDescriptionRequired' });
        expect(submittable('', '')).toBe(false);
    });

    it('refuses a name with nothing a directory can be called from it', () => {
        expect(validateExtensionCreateDraft({ name: '??? ---', description: 'does a thing' }))
            .toEqual({ name: 'settings.extensions.createNameUnusable' });
    });

    it("lets a name of any script through, because the slugifier is the server's", () => {
        expect(submittable('サイドバー時計', 'shows time')).toBe(true);
        expect(submittable('Уголок отзывов', 'shows time')).toBe(true);
    });

    it('submits the two answers a person owes', () => {
        expect(submittable('Sidebar clock', 'Show how long each task has been in Review.')).toBe(true);
    });
});

describe('the create\'s sandbox recommendation', () => {
    const options = (allowNetwork: boolean, defaultCapabilities: boolean) =>
        extensionCreateSandboxOptions({ allowNetwork, defaultCapabilities });

    it('keeps the project isolated and ungranted when neither is answered', () => {
        expect(options(false, false)).toEqual({ networkIsolation: true, capabilities: [] });
    });

    it('opens the network alone and grants no capability', () => {
        expect(options(true, false)).toEqual({ networkIsolation: false, capabilities: [] });
    });

    it('grants the capabilities alone and keeps the container off the network', () => {
        expect(options(false, true)).toEqual({
            networkIsolation: true,
            capabilities: [...DOCKER_DEFAULT_CAPABILITIES],
        });
    });

    it('sends Docker\'s own list, the one the Sandbox Configuration offers', () => {
        expect(options(true, true)).toEqual({
            networkIsolation: false,
            capabilities: [...DOCKER_DEFAULT_CAPABILITIES],
        });
    });

    it('hands out a list the caller cannot write through', () => {
        const first = options(false, true);
        first.capabilities.push('CAP_SYS_ADMIN');
        expect(options(false, true).capabilities).toEqual([...DOCKER_DEFAULT_CAPABILITIES]);
    });
});
