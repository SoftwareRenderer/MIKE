import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import { render, cleanup } from '@testing-library/svelte';
import ProviderCard from '$lib/components/ConfigModal/ProviderCard.svelte';
import { extensionManager } from '$lib/services/extension-manager.svelte';
import type { Extension } from '$lib/types';

// Mock svelte transitions (not available in jsdom)
vi.mock('svelte/transition', () => ({
    slide: vi.fn(),
    fade: vi.fn(),
}));

// Polyfill matchMedia for useIsMobile (desktop: never matches)
beforeAll(() => {
    vi.stubGlobal('matchMedia', (query: string) => ({
        matches: false,
        media: query,
        onchange: null,
        addEventListener: () => {},
        removeEventListener: () => {},
        addListener: () => {},
        removeListener: () => {},
        dispatchEvent: () => false,
    }));
});

function makeAgentExtension(): Extension {
    return {
        id: 'ext-1',
        name: 'pi',
        version: '1.0.0',
        type: 'agent',
        manifest: {
            display_name: 'Pi Coding Agent',
            description: 'Minimal agent harness',
            author: 'Pi',
            version: '1.0.0',
            capabilities: {
                agent: {
                    wasm_module: 'pi.wasm',
                    description: 'A coding agent',
                },
            },
        },
        config: {},
        container_config: {},
        sandbox_config: { network_isolation: true, memory_limit: 0 },
        install_source: 'registry',
        enabled: true,
        running: false,
        installed_at: '2025-01-01T00:00:00Z',
        built_at: '2025-01-01T00:00:00Z',
    };
}

function makeProvider(kind: 'llm' | 'agent') {
    return {
        name: kind === 'agent' ? 'pi' : '',
        display_name: kind === 'agent' ? 'Pi Coding Agent' : '',
        kind,
        url: kind === 'agent' ? 'agent://pi' : 'http://localhost:8080/v1',
        api_key: '',
        models: [],
        concurrency: 1,
        max_requests_per_second: 0,
        input_price: 0,
        output_price: 0,
        cache_read_price: 0,
        cache_write_price: 0,
        power_consumption_w: 0,
        electricity_cost_per_kwh: 0,
        fetchingModels: false,
        fetchError: '',
        expanded: true,
        testingConnection: false,
        testResult: null,
    };
}

function renderCard(kind: 'llm' | 'agent') {
    const provider = makeProvider(kind);
    return render(ProviderCard, {
        props: {
            providers: [provider],
            provider,
            index: 0,
            encryptionEnabled: false,
            currencySymbol: '$',
            getModelColor: () => '#000000',
            onDragStart: () => {},
            onRemove: () => {},
            onFetchModels: () => {},
            onTestConnection: () => {},
            onAddModel: () => {},
            onRemoveModel: () => {},
            isDragging: false,
            onDirty: () => {},
        },
    });
}

describe('ProviderCard — agent providers', () => {
    afterEach(() => {
        cleanup();
        (extensionManager.state as { extensions: Extension[] }).extensions = [];
    });

    it('renders a remove button for LLM providers', () => {
        const { container } = renderCard('llm');
        expect(container.querySelector('.btn-remove-provider')).toBeTruthy();
    });

    it('renders no remove button for agent providers', () => {
        const { container } = renderCard('agent');
        expect(container.querySelector('.btn-remove-provider')).toBeNull();
        // The drag handle and expand button remain (reordering is cosmetic).
        expect(container.querySelector('.btn-drag-handle')).toBeTruthy();
        expect(container.querySelector('.btn-expand')).toBeTruthy();
    });

    it('explains extension management on the agent card', () => {
        (extensionManager.state as { extensions: Extension[] }).extensions = [makeAgentExtension()];
        const { container } = renderCard('agent');
        const note = container.querySelector('.agent-managed-note');
        expect(note).toBeTruthy();
        expect(note!.textContent).toContain('Pi Coding Agent');
        expect(note!.textContent).toContain('Settings → Extensions');
    });

    it('shows the missing-extension state when the agent extension is not installed', () => {
        const { container } = renderCard('agent');
        expect(container.querySelector('.agent-info-missing')).toBeTruthy();
    });
});
