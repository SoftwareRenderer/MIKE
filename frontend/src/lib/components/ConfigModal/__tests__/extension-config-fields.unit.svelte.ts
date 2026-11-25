import { describe, it, expect, afterEach } from 'vitest';
import { render, cleanup, fireEvent } from '@testing-library/svelte';
import ExtensionConfigFields from '../ExtensionConfigFields.svelte';
import type { Extension } from '$lib/types';

/**
 * The control a declared list of values gets. The fallback a stored word outside the list belongs to
 * `initialEditConfig`; here it is that the field shows a declared option and writes one back.
 */

function extensionWith(properties: object[]): Extension {
    return {
        id: 'levels',
        name: 'levels',
        version: '1.0.0',
        type: 'ui',
        manifest: {
            display_name: 'Levels',
            description: '',
            author: '',
            version: '1.0.0',
            config_schema: { properties },
        },
        // Empty on purpose: the field reads the draft, so a shown value came from what Save will send.
        config: {},
        container_config: {},
        sandbox_config: {},
        install_source: 'registry',
        enabled: true,
        running: false,
        installed_at: '',
        built_at: '',
    } as unknown as Extension;
}

function renderFields(properties: object[], draft: Record<string, any> = {}) {
    return render(ExtensionConfigFields, { ext: extensionWith(properties), config: draft });
}

const LEVEL = {
    key: 'level',
    type: 'string',
    title: 'Verbosity',
    description: 'How much it logs.',
    enum: ['off', 'terse', 'verbose'],
    default: 'off',
};

function selectFor(container: HTMLElement, key: string): HTMLSelectElement | null {
    return container.querySelector(`select#edit-config-${key}`);
}

describe('a config property that declares its values', () => {
    afterEach(cleanup);

    it('is one option per declared value, opened on the stored one', () => {
        const { container } = renderFields([LEVEL], { level: 'terse' });
        const select = selectFor(container, 'level');
        expect(select).toBeTruthy();
        expect([...(select?.options ?? [])].map((o) => o.value)).toEqual(['off', 'terse', 'verbose']);
        expect(select?.value).toBe('terse');
    });

    it('shows the schema default when the draft holds a word the list does not declare', () => {
        // The default is deliberately not the first option: an unbound select opens on that one.
        const { container } = renderFields([{ ...LEVEL, default: 'terse' }], { level: 'chatty' });
        expect(selectFor(container, 'level')?.value).toBe('terse');
    });

    it('writes the picked value straight into the draft', async () => {
        const draft: Record<string, any> = { level: 'off' };
        const { container } = renderFields([LEVEL], draft);
        const select = selectFor(container, 'level') as HTMLSelectElement;
        await fireEvent.change(select, { target: { value: 'verbose' } });
        expect(draft.level).toBe('verbose');
    });

    it('is a pick-list rather than a password field even when the value is a secret', () => {
        const region = { key: 'region', type: 'string', title: 'Region', enum: ['frankfurt', 'ashburn'], default: 'frankfurt', sensitive: true };
        const { container } = renderFields([region], { region: 'ashburn' });
        expect(selectFor(container, 'region')).toBeTruthy();
        expect(container.querySelector('input#edit-config-region')).toBeNull();
    });

    it('leaves every other type the control its own type names', () => {
        const { container } = renderFields([
            { key: 'port', type: 'number', title: 'Port', enum: [8080, 9090], default: 8080 },
            { key: 'verbose', type: 'boolean', title: 'Log everything', default: false },
            { key: 'token', type: 'string', title: 'Token', sensitive: true },
        ]);
        expect(selectFor(container, 'port')).toBeNull();
        expect(container.querySelector('input[type="number"]#edit-config-port')).toBeTruthy();
        expect(container.querySelector('input[type="password"]#edit-config-token')).toBeTruthy();
        expect(container.querySelector('input[type="text"]')).toBeNull();
    });
});
