import { describe, it, expect, afterEach } from 'vitest';
import { render, cleanup } from '@testing-library/svelte';

import ProviderCard from '$lib/components/ConfigModal/ProviderCard.svelte';
import type { ProviderDraft } from '$lib/components/ConfigModal/drafts';

afterEach(cleanup);

/**
 * A dead control owes the reader a reason. Fetch Models and Test Connection go dark while the URL
 * field is empty, and a `title` is the one place that reason cannot live: it needs a hover, and a
 * disabled control is exactly what the pointer is not meant to reach. So the reason is a real
 * element beside the two buttons and each of them points at it. Rendered, not read off the source.
 */

// jsdom has no matchMedia; HelpIcon asks for it at construction.
const mq = { matches: false, media: '', addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {}, onchange: null, dispatchEvent: () => false };
window.matchMedia = () => mq;

function draft(overrides: Partial<ProviderDraft> = {}): ProviderDraft {
    return {
        uid: 'uid-1', id: 7, name: '', display_name: undefined, kind: 'llm',
        url: '', api_key: '', models: [],
        concurrency: 1, max_requests_per_second: 0, stream_idle_seconds: 0,
        input_price: 0, output_price: 0, cache_read_price: 0, cache_write_price: 0,
        power_consumption_w: 0, electricity_cost_per_kwh: 0,
        fetchingModels: false, fetchError: '', testingConnection: false, testResult: null,
        expanded: true,
        ...overrides,
    };
}

function renderCard(provider: ProviderDraft) {
    return render(ProviderCard, {
        props: {
            providers: [provider],
            provider,
            index: 0,
            currencySymbol: '$',
            isDragging: false,
            onDragStart: () => {},
            onRemove: () => {},
            onFetchModels: () => {},
            onTestConnection: () => {},
            onAddModel: () => {},
            onRemoveModel: () => {},
            onDirty: () => {},
        },
    });
}

/** The element an action describes, or null when it describes nothing / points at a dead id. */
function describedBy(container: ParentNode, el: Element | null): string | null {
    const id = el?.getAttribute('aria-describedby');
    if (!id) return null;
    return container.querySelector(`#${id}`)?.textContent?.trim() ?? '';
}

describe('ProviderCard: the two URL-dependent actions state why they are dead', () => {
    it('names the missing URL for both actions while the field is empty', () => {
        const { container } = renderCard(draft({ url: '' }));
        const fetch = container.querySelector('.btn-fetch');
        const test = container.querySelector('.btn-test');

        expect(fetch?.hasAttribute('disabled')).toBe(true);
        expect(test?.hasAttribute('disabled')).toBe(true);

        const reason = 'Enter a provider URL first';
        expect(describedBy(container, fetch)).toBe(reason);
        expect(describedBy(container, test)).toBe(reason);
    });

    it('says the same reason while a request is in flight and the URL is still empty', () => {
        const { container } = renderCard(draft({ url: '   ', fetchingModels: true, testingConnection: true }));
        const fetch = container.querySelector('.btn-fetch');
        const test = container.querySelector('.btn-test');

        expect(fetch?.hasAttribute('disabled')).toBe(true);
        expect(test?.hasAttribute('disabled')).toBe(true);
        expect(describedBy(container, fetch)).toBe('Enter a provider URL first');
        expect(describedBy(container, test)).toBe('Enter a provider URL first');
    });

    it('drops the reason and the description together once a URL is there', () => {
        const { container } = renderCard(draft({ url: 'http://localhost:8080/v1' }));
        const fetch = container.querySelector('.btn-fetch');
        const test = container.querySelector('.btn-test');

        expect(fetch?.hasAttribute('disabled')).toBe(false);
        expect(test?.hasAttribute('disabled')).toBe(false);
        expect(fetch?.hasAttribute('aria-describedby')).toBe(false);
        expect(test?.hasAttribute('aria-describedby')).toBe(false);
        expect(container.querySelector('.actions-hint')).toBeNull();
    });

    it('describes the wait, not the field, while a request runs on a filled URL', () => {
        const { container } = renderCard(draft({ url: 'http://localhost:8080/v1', fetchingModels: true }));
        const fetch = container.querySelector('.btn-fetch');

        expect(fetch?.hasAttribute('disabled')).toBe(true);
        expect(fetch?.hasAttribute('aria-describedby')).toBe(false);
        expect(container.querySelector('.actions-hint')).toBeNull();
    });
});
