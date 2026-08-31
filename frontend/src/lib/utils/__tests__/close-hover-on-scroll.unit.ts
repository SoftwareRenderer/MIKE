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

/** Fake scroll/view elements capturing the registered listeners. */
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
        fire: (type: string, init?: MouseEventInit) => {
            for (const cb of listeners[type] || []) cb(new Event(type, init) as Event);
        },
    };
}

const TOOLTIP_BOX = { left: 100, right: 200, top: 40, bottom: 70 };

/**
 * @param tooltip the open hover tooltip's client box, or null for none open.
 */
function makeView(tooltip: { left: number; right: number; top: number; bottom: number } | null = null): any {
    const domListeners: Record<string, EventListener[]> = {};
    return {
        scrollDOM: makeScrollDOM(),
        state: {},
        dispatch: vi.fn(),
        dom: {
            addEventListener: vi.fn((type: string, cb: EventListener) => {
                (domListeners[type] ||= []).push(cb);
            }),
            removeEventListener: vi.fn(),
            querySelector: vi.fn(() =>
                tooltip ? { getBoundingClientRect: () => tooltip } : null,
            ),
            /** Move the pointer, as the editor's mousemove listener sees it. */
            movePointer: (x: number, y: number) => {
                for (const cb of domListeners['mousemove'] || []) cb({ clientX: x, clientY: y } as MouseEvent);
            },
        },
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

    it('leaves the tooltip open when the scroll begins with the pointer inside it', () => {
        // A trackpad nudge while the pointer rests on the box must not dismiss
        // the thing being read — in this editor, the diagnostics hover.
        const view = makeView(TOOLTIP_BOX);
        const { scrollDOM } = createPlugin(view);
        hasHoverTooltipsMock.mockReturnValue(true);
        view.dom.movePointer(150, 55);

        scrollDOM.fire('scroll');

        expect(view.dispatch).not.toHaveBeenCalled();
    });

    it('still closes when the pointer is outside the tooltip', () => {
        const view = makeView(TOOLTIP_BOX);
        const { scrollDOM } = createPlugin(view);
        hasHoverTooltipsMock.mockReturnValue(true);
        view.dom.movePointer(150, 300);

        scrollDOM.fire('scroll');

        expect(view.dispatch).toHaveBeenCalledTimes(1);
    });

    it('closes on scroll before the pointer has been seen', () => {
        const view = makeView(TOOLTIP_BOX);
        const { scrollDOM } = createPlugin(view);
        hasHoverTooltipsMock.mockReturnValue(true);

        scrollDOM.fire('scroll');

        expect(view.dispatch).toHaveBeenCalledTimes(1);
    });

    it('tolerates a pointer just outside the tooltip box', () => {
        const view = makeView(TOOLTIP_BOX);
        const { scrollDOM } = createPlugin(view);
        hasHoverTooltipsMock.mockReturnValue(true);
        view.dom.movePointer(98, 55); // 2px left of the box, inside the margin

        scrollDOM.fire('scroll');

        expect(view.dispatch).not.toHaveBeenCalled();
    });
});
