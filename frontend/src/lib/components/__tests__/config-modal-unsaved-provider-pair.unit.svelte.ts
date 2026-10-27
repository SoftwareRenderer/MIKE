import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, fireEvent, cleanup, waitFor } from '@testing-library/svelte';
import ConfigModal from '$lib/components/ConfigModal.svelte';
import type { Config, Provider } from '$lib/types';
import { updateConfig, fetchModels } from '$lib/api';

vi.mock('$lib/api', async (importOriginal) => {
    const actual = await importOriginal<typeof import('$lib/api')>();
    return { ...actual, updateConfig: vi.fn(), fetchModels: vi.fn() };
});

const ADD = 'button[title="Add a new LLM provider"]';
const SAVE = 'button[title="Save settings"]';
const URL_ONE = 'https://first.example.com/v1';
const URL_TWO = 'https://second.example.com/v1';

beforeEach(() => {
    Object.defineProperty(Element.prototype, 'scrollIntoView', { configurable: true, value: () => {} });
    vi.stubGlobal('matchMedia', (query: string) => ({
        matches: false, media: query, onchange: null,
        addEventListener: () => {}, removeEventListener: () => {},
        addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false,
    }));
    vi.mocked(updateConfig).mockReset();
    vi.mocked(fetchModels).mockReset();
});

afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
});

function savedProvider(id: number): Provider {
    return { id, name: '', kind: 'llm', url: URL_ONE, api_key: '••••••••••', models: [{ name: 'gpt-test' }], concurrency: 1, max_requests_per_second: 0, stream_idle_seconds: 0 };
}

function makeConfig(providers: Provider[], pair?: { id: number; model: string }): Config {
    const ref = pair ? String(pair.id) : '';
    const model = pair ? pair.model : '';
    return {
        workspace_dir: '/tmp/wt', database_path: '/tmp/db', version: '1.0.0', providers,
        worker_provider: ref, worker_model: model, utility_provider: ref, utility_model: model,
    };
}

const settle = () => new Promise(r => setTimeout(r, 30));

function pairRow(container: HTMLElement, index: number): HTMLSelectElement[] {
    return Array.from(container.querySelectorAll('.primary-provider-row')[index].querySelectorAll('select'));
}

function optionValues(select: HTMLSelectElement): string[] {
    return Array.from(select.options).map(o => o.value);
}

async function addCard(container: HTMLElement): Promise<number> {
    const before = container.querySelectorAll('.provider-card').length;
    fireEvent.click(container.querySelector(ADD)!);
    await waitFor(() => expect(container.querySelectorAll('.provider-card').length).toBe(before + 1));
    return before;
}

async function connectCard(container: HTMLElement, index: number, url: string) {
    fireEvent.input(container.querySelector(`#provider-url-${index}`)!, { target: { value: url } });
    fireEvent.click(container.querySelectorAll('.btn-fetch')[index]);
    await settle();
}

async function selectPair(container: HTMLElement, providerValue: string, model: string) {
    for (const row of [0, 1]) {
        fireEvent.change(pairRow(container, row)[0], { target: { value: providerValue } });
        await settle();
        fireEvent.change(pairRow(container, row)[1], { target: { value: model } });
        await settle();
    }
}

describe('Worker/Utility pair naming a provider that is not saved yet', () => {
    it('tells two unsaved providers apart in the Worker selectors', async () => {
        vi.mocked(fetchModels).mockImplementation(async (url: string) =>
            url === URL_ONE ? { models: [{ name: 'model-one' }] } : { models: [{ name: 'model-two' }] });
        const { container } = render(ConfigModal, {
            props: { config: makeConfig([]), onClose: vi.fn(), onSaved: vi.fn() },
        });
        await settle();

        const first = await addCard(container);
        const second = await addCard(container);
        await connectCard(container, first, URL_ONE);
        await connectCard(container, second, URL_TWO);
        await waitFor(() => expect(container.querySelectorAll('.model-row').length).toBe(2));

        const refs = optionValues(pairRow(container, 0)[0]).filter(Boolean);
        expect(refs).toHaveLength(2);
        expect(refs[0]).not.toBe(refs[1]);
        expect(refs).not.toContain('0');

        fireEvent.change(pairRow(container, 0)[0], { target: { value: refs[1] } });
        await settle();
        expect(optionValues(pairRow(container, 0)[1]).filter(Boolean)).toEqual(['model-two']);
    });

    it('sends the provider first and the pair with the ID that came back', async () => {
        vi.mocked(fetchModels).mockResolvedValue({ models: [{ name: 'gpt-test' }] });
        vi.mocked(updateConfig)
            .mockResolvedValueOnce(makeConfig([savedProvider(7)]))
            .mockResolvedValueOnce(makeConfig([savedProvider(7)], { id: 7, model: 'gpt-test' }));
        const onSaved = vi.fn();
        const { container } = render(ConfigModal, {
            props: { config: makeConfig([]), onClose: vi.fn(), onSaved },
        });
        await settle();

        const card = await addCard(container);
        await connectCard(container, card, URL_ONE);
        await waitFor(() => expect(container.querySelectorAll('.model-row').length).toBe(1));
        await selectPair(container, optionValues(pairRow(container, 0)[0]).filter(Boolean)[0], 'gpt-test');

        fireEvent.click(container.querySelector(SAVE)!);
        await waitFor(() => expect(updateConfig).toHaveBeenCalledTimes(2), { timeout: 2000 });

        const [providersBody, pairBody] = vi.mocked(updateConfig).mock.calls.map(c => c[0] as any);
        expect(providersBody.worker_provider).toBeUndefined();
        expect(providersBody.utility_provider).toBeUndefined();
        expect(pairBody).toMatchObject({
            worker_provider: '7', worker_model: 'gpt-test',
            utility_provider: '7', utility_model: 'gpt-test',
        });
        await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1), { timeout: 2000 });
    });

    it('saves in one request when the pair already names a saved provider', async () => {
        vi.mocked(updateConfig).mockResolvedValue(makeConfig([savedProvider(3)], { id: 3, model: 'gpt-test' }));
        const { container } = render(ConfigModal, {
            props: { config: makeConfig([savedProvider(3)], { id: 3, model: 'gpt-test' }), onClose: vi.fn(), onSaved: vi.fn() },
        });
        await settle();

        fireEvent.click(container.querySelector(SAVE)!);
        await waitFor(() => expect(updateConfig).toHaveBeenCalledTimes(1), { timeout: 2000 });
        expect(vi.mocked(updateConfig).mock.calls[0][0] as any).toMatchObject({ worker_provider: '3' });
    });

    it('refuses a pair naming a provider with no address, before any request', async () => {
        const { container } = render(ConfigModal, {
            props: { config: makeConfig([]), onClose: vi.fn(), onSaved: vi.fn() },
        });
        await settle();
        await addCard(container);
        fireEvent.click(container.querySelector('.btn-add-model')!);
        await settle();
        fireEvent.input(container.querySelector('.model-input')!, { target: { value: 'by-hand' } });
        await settle();
        await selectPair(container, optionValues(pairRow(container, 0)[0]).filter(Boolean)[0], 'by-hand');

        fireEvent.click(container.querySelector(SAVE)!);
        await settle();
        expect(updateConfig).not.toHaveBeenCalled();
        expect(container.querySelector('#config-error')?.textContent).toContain('has no URL');
    });

    it('keeps the issued ID when the pair write is retried after a failure', async () => {
        vi.mocked(fetchModels).mockResolvedValue({ models: [{ name: 'gpt-test' }] });
        vi.mocked(updateConfig)
            .mockResolvedValueOnce(makeConfig([savedProvider(7)]))
            .mockRejectedValueOnce(new Error('connection reset'))
            .mockResolvedValueOnce(makeConfig([savedProvider(7)], { id: 7, model: 'gpt-test' }));
        const onSaved = vi.fn();
        const { container } = render(ConfigModal, {
            props: { config: makeConfig([]), onClose: vi.fn(), onSaved },
        });
        await settle();

        const card = await addCard(container);
        await connectCard(container, card, URL_ONE);
        await waitFor(() => expect(container.querySelectorAll('.model-row').length).toBe(1));
        await selectPair(container, optionValues(pairRow(container, 0)[0]).filter(Boolean)[0], 'gpt-test');

        fireEvent.click(container.querySelector(SAVE)!);
        await waitFor(() => expect(updateConfig).toHaveBeenCalledTimes(2), { timeout: 2000 });
        await waitFor(() => expect(container.querySelector('#config-error')?.textContent)
            .toContain('The Worker and Utility defaults were not saved.'));
        expect(onSaved).not.toHaveBeenCalled();

        fireEvent.click(container.querySelector(SAVE)!);
        await waitFor(() => expect(updateConfig).toHaveBeenCalledTimes(3), { timeout: 2000 });
        const retry = vi.mocked(updateConfig).mock.calls[2][0] as any;
        expect(retry.providers[0].id).toBe(7);
        expect(retry.worker_provider).toBe('7');
        expect(retry.utility_provider).toBe('7');
        await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1), { timeout: 2000 });
    });
});
