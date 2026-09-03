import { describe, it, expect, afterEach } from 'vitest';
import { render, cleanup, fireEvent } from '@testing-library/svelte';
import CustomSearchPanel from '../CustomSearchPanel.svelte';

/**
 * The find/replace panel over a fake CodeMirror view. Everything here is asserted
 * through the rendered DOM — the panel's contract with the user is what its inputs,
 * its counter and its buttons do, not which identifiers the component happens to use.
 */
function makeMockView(doc: string) {
    return {
        state: { doc: { toString: () => doc } },
        dispatch: () => { },
        coordsAtPos: () => null,
        scrollDOM: { clientHeight: 400, scrollTop: 0 },
    } as any;
}

function renderPanel(onFocusFind: (fn: (() => void) | null) => void, doc = 'hello world') {
    return render(CustomSearchPanel, {
        view: makeMockView(doc),
        onClose: () => { },
        onFocusFind,
    });
}

describe('CustomSearchPanel structure', () => {
    afterEach(cleanup);

    const noop = () => { };

    it('offers a find input, a match counter and match navigation', () => {
        const { container } = renderPanel(noop);

        expect(container.querySelector('.search-panel')).toBeTruthy();
        expect(container.querySelector('.search-row')).toBeTruthy();
        expect(container.querySelector('.search-nav')).toBeTruthy();
        expect(container.querySelector<HTMLInputElement>('input[placeholder="Find"]')).toBeTruthy();
        expect(container.querySelector('.match-count')).toBeTruthy();

        const nav = [...container.querySelectorAll('.nav-btn')];
        expect(nav.map(b => b.getAttribute('aria-label'))).toEqual(
            expect.arrayContaining(['Previous match', 'Next match', 'Close search']));
    });

    it('counts matches in the document as the query is typed', async () => {
        const { container } = renderPanel(noop);
        const input = container.querySelector<HTMLInputElement>('input[placeholder="Find"]')!;

        await fireEvent.input(input, { target: { value: 'o' } });
        expect(container.querySelector('.match-count')?.textContent).toBe('2 matches');

        await fireEvent.input(input, { target: { value: 'world' } });
        expect(container.querySelector('.match-count')?.textContent).toBe('1 match');

        await fireEvent.input(input, { target: { value: 'zzz' } });
        expect(container.querySelector('.match-count')?.textContent).toBe('0 matches');
    });

    it('closes on Escape and from the close button', async () => {
        let closed = 0;
        const utils = render(CustomSearchPanel, {
            view: makeMockView('hello world'),
            onClose: () => { closed++; },
            onFocusFind: () => { },
        });
        const input = utils.container.querySelector<HTMLInputElement>('input[placeholder="Find"]')!;

        await fireEvent.keyDown(input, { key: 'Escape' });
        expect(closed).toBe(1);

        const closeBtn = utils.container.querySelector<HTMLButtonElement>('button[aria-label="Close search"]')!;
        await fireEvent.click(closeBtn);
        expect(closed).toBe(2);
    });

    it('keeps replace hidden until it is asked for, then offers a replace input', async () => {
        const { container } = renderPanel(noop);
        expect(container.querySelector('.replace-row')).toBeNull();

        await fireEvent.click(container.querySelector<HTMLButtonElement>('.toggle-replace-btn')!);

        expect(container.querySelector('.replace-row')).toBeTruthy();
        expect(container.querySelector('input[placeholder="Replace"]')).toBeTruthy();
    });
});

describe('CustomSearchPanel focus contract', () => {
    afterEach(cleanup);

    it('registers a focus function that focuses the find input', () => {
        let focusFind: (() => void) | null | undefined;
        const { container } = renderPanel((fn) => { focusFind = fn; });

        expect(focusFind).toBeDefined();
        expect(typeof focusFind).toBe('function');

        // Move focus away (simulates the user clicking into the code editor)
        const other = document.createElement('button');
        container.appendChild(other);
        other.focus();
        expect(document.activeElement).toBe(other);

        focusFind!();
        const findInput = container.querySelector('input[placeholder="Find"]') as HTMLInputElement;
        expect(document.activeElement).toBe(findInput);
    });

    it('focuses the find input (not replace) when the replace row is open', async () => {
        let focusFind: (() => void) | null | undefined;
        const { container } = renderPanel((fn) => { focusFind = fn; });

        // Open the replace row
        container.querySelector('.toggle-replace-btn')!.dispatchEvent(new MouseEvent('click', { bubbles: true }));
        // Let Svelte flush the re-render from the DOM event
        await new Promise((r) => setTimeout(r, 0));
        const replaceInput = container.querySelector('.replace-input') as HTMLInputElement | null;
        expect(replaceInput).not.toBeNull();

        focusFind!();
        const findInput = container.querySelector('input[placeholder="Find"]') as HTMLInputElement;
        expect(document.activeElement).toBe(findInput);
        expect(document.activeElement).not.toBe(replaceInput);
    });

    it('unregisters the focus function on destroy', () => {
        let focusFind: (() => void) | null | undefined;
        const { unmount } = renderPanel((fn) => { focusFind = fn; });

        expect(typeof focusFind).toBe('function');
        unmount();
        expect(focusFind).toBeNull();
    });
});
