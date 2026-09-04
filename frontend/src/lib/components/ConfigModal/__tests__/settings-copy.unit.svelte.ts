import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, cleanup, waitFor } from '@testing-library/svelte';
import ConfigModal from '$lib/components/ConfigModal.svelte';

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

/**
 * The English values are the contract: every locator in the config-modal / preferences /
 * settings-* e2e specs selects on one of them, so a translation that lands on a different
 * English string is a broken build, not a translation choice. This renders the real modal
 * (no `readFileSync` string matching) and asserts the composites too — `{title} provider`
 * is the part a naive conversion loses.
 */
const mq = { matches: false, media: '', addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {}, onchange: null, dispatchEvent: () => false };
// jsdom has no matchMedia; this stub is what the preferences tab reads. (No
// `@ts-expect-error` here: the assignment typechecks, and a directive that
// suppresses nothing is itself an error under `npm run check`.)
window.matchMedia = () => mq;

function fetchOk(payload: unknown) {
    return vi.fn(async () => new Response(JSON.stringify(payload), { status: 200, headers: { 'Content-Type': 'application/json' } }));
}

describe('Settings shell renders translated copy', () => {
    it('tabs, footer and aria composites are the English copy', async () => {
        vi.stubGlobal('fetch', fetchOk({}));
        const { container } = render(ConfigModal, { props: { config: null, onClose: () => {}, onSaved: () => {} } });
        await waitFor(() => expect(container.querySelector('.tab-btn')).toBeTruthy());
        const tabs = [...container.querySelectorAll('.tab-btn')].map(b => b.textContent?.trim());
        expect(tabs).toEqual(['Providers', 'Task Defaults', 'Preferences', 'Extensions', 'About']);
        expect(container.querySelector('#modal-title')?.textContent).toBe('Settings');
        const titles = [...container.querySelectorAll('.primary-provider-select')].map(s => s.getAttribute('aria-label'));
        expect(titles).toEqual(['Worker provider', 'Worker model', 'Utility provider', 'Utility model']);
        const save = container.querySelector('.btn-primary');
        expect(save?.getAttribute('title')).toBe('Save settings');
        expect(save?.textContent?.trim()).toBe('Save');
        expect(container.querySelector('.close-btn')?.getAttribute('aria-label')).toBe('Close');
    });
});
