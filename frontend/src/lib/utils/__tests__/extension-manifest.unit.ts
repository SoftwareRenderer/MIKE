import { describe, it, expect } from 'vitest';
import {
    typeBadge,
    capabilityKeys,
    registryCapabilityKeys,
    registryCapabilities,
    capabilityDescriptions,
    injectionPointLabels,
    packLocale,
    formatBuiltAt,
} from '../extension-manifest';
import { t } from '$lib/i18n';

describe('typeBadge', () => {
    it('names every type the host knows', () => {
        expect(typeBadge('lsp')).toBe('LSP');
        expect(typeBadge('css')).toBe('CSS');
        expect(typeBadge('agent')).toBe('AGENT');
        expect(typeBadge('llm')).toBe('LLM');
        expect(typeBadge('tool')).toBe('TOOL');
    });

    it('gives an unknown type the UI badge, but never it to a translation pack', () => {
        expect(typeBadge('theme')).toBe('UI');
        expect(typeBadge('')).toBe('UI');
        expect(typeBadge('i18n')).toBe('I18N');
    });
});

describe('capabilityKeys — what the manifest declared, in badge order', () => {
    it('lists the declared capabilities and nothing else', () => {
        expect(capabilityKeys({ manifest: { capabilities: { ui: {}, network: {} } } })).toEqual(['ui', 'network']);
        expect(capabilityKeys({ manifest: { capabilities: { lsp: {}, i18n: {} } } })).toEqual(['lsp', 'i18n']);
    });

    it('is empty for a manifest that declares none', () => {
        expect(capabilityKeys({ manifest: { capabilities: {} } })).toEqual([]);
        expect(capabilityKeys({ manifest: {} })).toEqual([]);
    });
});

describe('the registry entry\'s capabilities', () => {
    const DECLARED = { ui: { injection_points: ['taskChat'] }, network: { allowed_domains: [] } };

    it('keeps the declared capabilities, so Browse shows what Installed shows', () => {
        expect(registryCapabilityKeys({ type: 'ui', capabilities: DECLARED })).toEqual(['ui', 'network']);
        expect(registryCapabilities({ capabilities: DECLARED })).toBe(DECLARED);
    });

    it('drops a capability name no description exists for', () => {
        expect(registryCapabilityKeys({ type: 'ui', capabilities: { preview: {} } })).toEqual([]);
    });

    it('falls back to the capability the type implies when the entry declares none', () => {
        expect(registryCapabilityKeys({ type: 'lsp' })).toEqual(['lsp']);
        expect(registryCapabilityKeys({ type: 'theme' })).toEqual([]);
        expect(registryCapabilities({ type: 'agent' })).toEqual({ agent: {} });
    });
});

describe('the label dictionaries', () => {
    it('describes every capability the badge row can offer', () => {
        const caps = capabilityDescriptions();
        expect(Object.keys(caps).sort()).toEqual(
            ['agent', 'css', 'i18n', 'llm', 'lsp', 'network', 'tools', 'ui'],
        );
        for (const [name, info] of Object.entries(caps)) {
            expect(info.label, name).toBeTruthy();
            expect(info.description, name).toBeTruthy();
        }
    });

    it('names each injection point a ui manifest can declare', () => {
        const points = injectionPointLabels();
        expect(Object.keys(points).sort()).toEqual(
            ['createTask', 'fileBrowser', 'fileEditor', 'projectBoard', 'projectSidebar', 'taskCard', 'taskChat'],
        );
        for (const [point, label] of Object.entries(points)) {
            expect(label, point).toBeTruthy();
        }
    });
});

describe('packLocale', () => {
    it('carries the tag the pack wrote, verbatim', () => {
        expect(packLocale({ i18n: { locale: 'pt-BR' } })).toBe('pt-BR');
    });

    it('is empty for anything that is not a string tag', () => {
        expect(packLocale({})).toBe('');
        expect(packLocale(undefined)).toBe('');
        expect(packLocale({ i18n: { locale: 7 } })).toBe('');
    });
});

describe('formatBuiltAt', () => {
    it('says not-built rather than an age for no build', () => {
        expect(formatBuiltAt('')).toBe(t('settings.extensions.notBuilt'));
    });

    it('renders a build date rather than an empty string', () => {
        expect(formatBuiltAt('2026-03-01T10:00:00Z')).toBeTruthy();
    });
});
