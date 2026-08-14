import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';

// Mock hasHoverTooltips so we can drive whether a tooltip is "active" without a
// real DOM/EditorView. closeHoverTooltips is a plain StateEffect we can inspect.
vi.mock('@codemirror/view', async (importOriginal) => {
    const actual = await importOriginal<typeof import('@codemirror/view')>();
    return {
        ...actual,
        hasHoverTooltips: vi.fn(() => false),
    };
});

import { hasHoverTooltips, closeHoverTooltips } from '@codemirror/view';
import { closeHoverOnScroll } from '../close-hover-on-scroll';

const hasHoverTooltipsMock = vi.mocked(hasHoverTooltips);

/** Fake scroll element capturing the registered listeners. */
function makeScrollDOM() {
    const listeners: Record<string, EventListener[]> = {};
    return {
        addEventListener: vi.fn((type: string, cb: EventListener) => {
            (listeners[type] ||= []).push(cb);
        }),
        removeEventListener: vi.fn((type: string, cb: EventListener) => {
            const arr = listeners[type] || [];
            const i = arr.indexOf(cb);
            if (i >= 0) arr.splice(i, 1);
        }),
        fire: (type: string) => {
            for (const cb of listeners[type] || []) cb(new Event(type) as Event);
        },
    };
}

function makeView(): any {
    return {
        scrollDOM: makeScrollDOM(),
        state: {},
        dispatch: vi.fn(),
    };
}

function createPlugin(view: any) {
    const plugin = closeHoverOnScroll();
    return { instance: (plugin as any).create(view), scrollDOM: view.scrollDOM };
}

beforeEach(() => {
    hasHoverTooltipsMock.mockClear();
});

afterEach(() => {
    vi.restoreAllMocks();
});

describe('closeHoverOnScroll', () => {
    it('closes hover tooltips when the editor scrolls and tooltips are active', () => {
        const view = makeView();
        const { scrollDOM } = createPlugin(view);
        hasHoverTooltipsMock.mockReturnValue(true);

        scrollDOM.fire('scroll');

        expect(hasHoverTooltipsMock).toHaveBeenCalledWith(view.state);
        expect(view.dispatch).toHaveBeenCalledTimes(1);
        const effects = view.dispatch.mock.calls[0][0].effects;
        // dispatch() accepts a single effect or an array of effects.
        expect(Array.isArray(effects) ? effects : [effects]).toContain(closeHoverTooltips);
    });

    it('does not dispatch when scrolling but no hover tooltip is open', () => {
        const view = makeView();
        const { scrollDOM } = createPlugin(view);
        hasHoverTooltipsMock.mockReturnValue(false);

        scrollDOM.fire('scroll');

        expect(view.dispatch).not.toHaveBeenCalled();
    });

    it('removes the scroll listener on destroy so later scrolls do nothing', () => {
        const view = makeView();
        const { instance, scrollDOM } = createPlugin(view);

        instance.destroy();
        expect(scrollDOM.removeEventListener).toHaveBeenCalledWith('scroll', expect.any(Function));

        hasHoverTooltipsMock.mockReturnValue(true);
        scrollDOM.fire('scroll');
        expect(view.dispatch).not.toHaveBeenCalled();
    });
});
