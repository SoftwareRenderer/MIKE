/**
 * THE ONE EMPTY STATE
 *
 * A pane with nothing in it says so with one column of text, and the column holds only what the
 * caller handed over: a headline alone is a complete empty state, and a hint, a glyph or a call to
 * action appear exactly when they are given. The column is not a live region — an empty state
 * describes a pane rather than a change, so a screen reader must hear it as part of the pane
 * instead of as news each time a list empties.
 */
import { describe, it, expect, afterEach } from 'vitest';
import { render, cleanup } from '@testing-library/svelte';
import { createRawSnippet } from 'svelte';
import EmptyState from '$lib/components/EmptyState.svelte';
import folderIcon from '$lib/assets/icons/folder.svg?raw';

afterEach(cleanup);

const pane = (container: HTMLElement) => container.querySelector('.empty-state') as HTMLElement;

describe('EmptyState', () => {
    it.each([
        { given: { title: 'No commits' }, title: 'No commits', hint: null, glyph: false },
        {
            given: { title: 'No extensions installed', hint: 'Install one from the registry.', icon: folderIcon },
            title: 'No extensions installed', hint: 'Install one from the registry.', glyph: true,
        },
    ])('paints only the parts it was given', ({ given, title, hint, glyph }) => {
        const { container } = render(EmptyState, { props: given });
        expect(pane(container).querySelector('.title')?.textContent).toBe(title);
        expect(pane(container).querySelector('.hint')?.textContent ?? null).toBe(hint);
        expect(pane(container).querySelector('.action')).toBeNull();
        expect(pane(container).querySelector('svg') !== null).toBe(glyph);
        // The glyph stands in for the headline, so only the words are read.
        expect(pane(container).querySelector('.icon')?.getAttribute('aria-hidden') ?? null).toBe(glyph ? 'true' : null);
    });

    it('renders the call to action through the action snippet', () => {
        const action = createRawSnippet(() => ({ render: () => '<button type="button">Browse</button>' }));
        const { container } = render(EmptyState, { props: { title: 'No extensions installed', action } });
        expect(pane(container).querySelector('.action button')?.textContent).toBe('Browse');
    });

    it('announces nothing, because an empty pane is not an event', () => {
        const { container } = render(EmptyState, { props: { title: 'No commits', hint: 'Commit once and this goes away.' } });
        expect(pane(container).hasAttribute('role')).toBe(false);
        expect(pane(container).hasAttribute('aria-live')).toBe(false);
    });

    it('takes the tighter rhythm and the caller layout hook on the same element it styles', () => {
        const { container } = render(EmptyState, { props: { title: 'No symbols', compact: true, class: 'pane-fill' } });
        expect(pane(container).classList.contains('compact')).toBe(true);
        expect(pane(container).classList.contains('pane-fill')).toBe(true);
    });
});
