import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, fireEvent, cleanup, screen } from '@testing-library/svelte';
import HelpIcon from '$lib/components/ConfigModal/HelpIcon.svelte';

const TIP = 'Max tasks running concurrently on this provider; also caps in-flight LLM requests to this number.';

type MqlListener = () => void;

/** Stub window.matchMedia for the `(hover: none)` touch query. */
function stubTouch(touch: boolean) {
    const listeners: MqlListener[] = [];
    const mql = {
        matches: touch,
        media: '(hover: none)',
        onchange: null,
        addEventListener: (_: string, fn: MqlListener) => listeners.push(fn),
        removeEventListener: () => {},
        addListener: () => {},
        removeListener: () => {},
        dispatchEvent: () => false,
    };
    vi.stubGlobal('matchMedia', vi.fn(() => mql));
    return {
        setTouch: (now: boolean) => {
            mql.matches = now;
            listeners.forEach((l) => l());
        },
    };
}

/** Flush Svelte 5 effect scheduling (dismissing listeners register in an effect). */
const flush = () => new Promise((r) => setTimeout(r, 0));

const hasClass = (el: Element | null, name: string) => !!el?.classList.contains(name);

function renderIcon() {
    return render(HelpIcon, { props: { text: TIP, label: 'Concurrency' } });
}

afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
});

describe('HelpIcon — hover-capable devices (native title)', () => {
    it('renders a span with the native title and no tooltip button', () => {
        stubTouch(false);
        const { container } = renderIcon();
        const icon = container.querySelector('.help-icon');
        expect(icon).not.toBeNull();
        expect(icon?.tagName).toBe('SPAN');
        expect(icon?.getAttribute('title')).toBe(TIP);
        expect(screen.queryByRole('button')).toBeNull();
        expect(container.querySelector('.help-tooltip')).toBeNull();
    });
});

describe('HelpIcon — touch devices (press to show tooltip)', () => {
    it('renders a pressable button, hidden tooltip, and shows the message on press', async () => {
        stubTouch(true);
        renderIcon();
        const btn = screen.getByRole('button', { name: 'Concurrency help' });
        const tooltip = screen.getByRole('tooltip');
        expect(hasClass(tooltip, 'visible')).toBe(false);
        expect(btn.getAttribute('aria-expanded')).toBe('false');

        await fireEvent.click(btn);
        expect(btn.getAttribute('aria-expanded')).toBe('true');
        expect(hasClass(tooltip, 'visible')).toBe(true);
        expect(tooltip.textContent).toBe(TIP);

        // Re-press dismisses.
        await fireEvent.click(btn);
        expect(hasClass(tooltip, 'visible')).toBe(false);
        expect(btn.getAttribute('aria-expanded')).toBe('false');
    });

    it('dismisses on outside press', async () => {
        stubTouch(true);
        renderIcon();
        const btn = screen.getByRole('button', { name: 'Concurrency help' });
        const tooltip = screen.getByRole('tooltip');

        await fireEvent.click(btn);
        expect(hasClass(tooltip, 'visible')).toBe(true);
        await flush(); // dismissing listeners register in an effect

        // State writes from the raw (non-Svelte) document listener flush on a microtask.
        fireEvent.pointerDown(document.body);
        await flush();
        expect(hasClass(tooltip, 'visible')).toBe(false);
    });

    it('stays open when pressing the icon itself', async () => {
        stubTouch(true);
        renderIcon();
        const btn = screen.getByRole('button', { name: 'Concurrency help' });
        const tooltip = screen.getByRole('tooltip');

        await fireEvent.click(btn);
        expect(hasClass(tooltip, 'visible')).toBe(true);
        await flush();

        // pointerdown on the button must not dismiss (the click after it re-toggles).
        fireEvent.pointerDown(btn);
        expect(hasClass(tooltip, 'visible')).toBe(true);
    });

    it('dismisses on Escape', async () => {
        stubTouch(true);
        renderIcon();
        const btn = screen.getByRole('button', { name: 'Concurrency help' });
        const tooltip = screen.getByRole('tooltip');

        await fireEvent.click(btn);
        expect(hasClass(tooltip, 'visible')).toBe(true);
        await flush();

        fireEvent.keyDown(document.body, { key: 'Escape' });
        await flush();
        expect(hasClass(tooltip, 'visible')).toBe(false);
    });

    it('stays open (repositioned) when the settings modal body scrolls', async () => {
        // The modal smooth-scrolls when a new provider card is revealed —
        // that programmatic scroll can settle while the user just pressed the
        // icon, so scrolling must reposition the bubble, not dismiss it.
        stubTouch(true);
        renderIcon();
        const btn = screen.getByRole('button', { name: 'Concurrency help' });
        const tooltip = screen.getByRole('tooltip');

        await fireEvent.click(btn);
        expect(hasClass(tooltip, 'visible')).toBe(true);
        await flush();

        fireEvent(window, new Event('scroll'));
        await flush();
        expect(hasClass(tooltip, 'visible')).toBe(true);
    });

    it('switches back to the native title span when hover support returns', async () => {
        const stub = stubTouch(true);
        const { container } = renderIcon();
        expect(container.querySelector('.help-icon')?.getAttribute('aria-label')).toBe('Concurrency help');

        stub.setTouch(false);
        await flush();

        const icon = container.querySelector('.help-icon');
        expect(icon?.tagName).toBe('SPAN');
        expect(icon?.getAttribute('title')).toBe(TIP);
        expect(container.querySelector('.help-tooltip')).toBeNull();
    });
});
