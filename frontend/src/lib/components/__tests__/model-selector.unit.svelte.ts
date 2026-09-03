import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, cleanup, fireEvent } from '@testing-library/svelte';
import ModelSelector from '../Chat/ModelSelector.svelte';
import { getProviderDisplayName } from '$lib/utils/providers';
import type { Provider } from '$lib/types';

vi.mock('svelte/transition', () => ({
    slide: vi.fn(),
    fade: vi.fn(),
}));

const LLM_A: Provider = {
    id: 1, name: 'alpha-provider', url: 'http://a:8010/v1', kind: 'llm',
    models: [{ name: 'zeta' }, { name: 'alpha' }], concurrency: 1, max_requests_per_second: 0,
} as unknown as Provider;

const LLM_B: Provider = {
    id: 2, name: 'beta-provider', url: 'http://b:8010/v1', kind: 'llm',
    models: [{ name: 'only-one' }], concurrency: 1, max_requests_per_second: 0,
} as unknown as Provider;

const AGENT: Provider = {
    name: 'pi', kind: 'agent', url: 'agent://pi', display_name: 'Pi Coding Agent',
    models: null as unknown as Provider['models'], concurrency: 1, max_requests_per_second: 0,
} as unknown as Provider;

function renderSelector(props: Partial<Record<string, unknown>> = {}) {
    const onSelect = vi.fn();
    const utils = render(ModelSelector, {
        props: {
            providers: [LLM_A, LLM_B],
            currentModel: 'zeta',
            currentUrl: LLM_A.url,
            onSelect,
            ...props,
        },
    });
    const q = (sel: string) => utils.container.querySelector(sel) as HTMLElement | null;
    return {
        ...utils,
        onSelect,
        providerTrigger: () => q('button[aria-label="Select provider"]'),
        modelTrigger: () => q('button[aria-label="Select model"]'),
    };
}

/** Dropdowns are portalled to the document, not the component container. */
const portal = (label: string) =>
    document.querySelector(`[role="listbox"][aria-label="${label}"]`) as HTMLElement | null;

afterEach(() => {
    cleanup();
    document.body.innerHTML = '';
});

describe('ModelSelector triggers', () => {
    it('offers a provider and a model trigger when there is a choice to make', () => {
        const { providerTrigger, modelTrigger } = renderSelector();
        expect(providerTrigger()).toBeTruthy();
        expect(modelTrigger()).toBeTruthy();
        expect(providerTrigger()?.getAttribute('aria-haspopup')).toBe('listbox');
    });

    it('names the current model on the trigger, without its org prefix', () => {
        const { modelTrigger } = renderSelector({ currentModel: 'org/gpt-4o' });
        expect(modelTrigger()?.textContent).toContain('gpt-4o');
    });

    it('prompts for a model when none is chosen', () => {
        const { modelTrigger } = renderSelector({ currentModel: '' });
        expect(modelTrigger()?.textContent).toContain('Select model');
    });

    it('offers no provider trigger when one provider is the only option', () => {
        const { providerTrigger, modelTrigger } = renderSelector({ providers: [LLM_A] });
        expect(providerTrigger()).toBeNull();
        expect(modelTrigger()).toBeTruthy();
    });

    it('survives an agent provider whose model list is null (older servers)', () => {
        const { container } = renderSelector({ providers: [LLM_A, AGENT], currentUrl: LLM_A.url });
        expect(container.querySelector('.model-selector')).toBeTruthy();
    });
});

describe('ModelSelector provider dropdown', () => {
    it('lists every provider with how many models it has', async () => {
        const { providerTrigger } = renderSelector();
        await fireEvent.click(providerTrigger()!);

        const list = portal('Select provider');
        expect(list).toBeTruthy();
        const names = [...list!.querySelectorAll('.provider-name')].map(n => n.textContent?.trim());
        expect(names).toEqual(expect.arrayContaining([
            getProviderDisplayName(LLM_A, [LLM_A, LLM_B]),
            getProviderDisplayName(LLM_B, [LLM_A, LLM_B]),
        ]));
        const counts = [...list!.querySelectorAll('.model-count')].map(n => n.textContent?.trim());
        expect(counts).toEqual(expect.arrayContaining(['2 models', '1 model']));
    });

    it('lists an agent provider alongside the LLM ones', async () => {
        const { providerTrigger } = renderSelector({ providers: [LLM_A, AGENT] });
        await fireEvent.click(providerTrigger()!);

        const list = portal('Select provider');
        expect(list!.querySelector('.provider-option-agent')).toBeTruthy();
    });

    it('closes when its trigger is clicked again', async () => {
        const { providerTrigger } = renderSelector();
        await fireEvent.click(providerTrigger()!);
        expect(portal('Select provider')).toBeTruthy();

        await fireEvent.click(providerTrigger()!);
        expect(portal('Select provider')).toBeNull();
    });
});

describe('ModelSelector model dropdown', () => {
    it('lists the current provider\'s models alphabetically', async () => {
        const { modelTrigger } = renderSelector();
        await fireEvent.click(modelTrigger()!);

        const list = portal('Select model');
        expect(list).toBeTruthy();
        const models = [...list!.querySelectorAll('.option-name')].map(n => n.textContent?.trim());
        expect(models).toEqual(['alpha', 'zeta']);
    });

    it('reports the chosen model with its provider reference and url', async () => {
        const { modelTrigger, onSelect } = renderSelector();
        await fireEvent.click(modelTrigger()!);

        const list = portal('Select model')!;
        const zeta = [...list.querySelectorAll('button')].find(b => b.textContent?.includes('zeta'))!;
        await fireEvent.click(zeta);

        expect(onSelect).toHaveBeenCalledWith(String(LLM_A.id), 'zeta', LLM_A.url, 'default');
    });

    it('marks the current model as selected', async () => {
        const { modelTrigger } = renderSelector();
        await fireEvent.click(modelTrigger()!);

        const list = portal('Select model')!;
        const selected = [...list.querySelectorAll('[aria-selected="true"]')];
        expect(selected).toHaveLength(1);
        expect(selected[0].textContent).toContain('zeta');
    });

    it('says so when the provider has no models to list', async () => {
        const empty: Provider = {
            id: 3, name: 'empty', url: 'http://c/v1', kind: 'llm', models: [],
            concurrency: 1, max_requests_per_second: 0,
        } as unknown as Provider;
        const { onSelect } = renderSelector({ providers: [LLM_A, empty], currentUrl: empty.url, currentModel: '' });

        const modelBtn = document.querySelector('button[aria-label="Select model"]') as HTMLElement;
        expect(modelBtn).toBeTruthy();
        await fireEvent.click(modelBtn);
        expect(onSelect).not.toHaveBeenCalled();
    });
});
