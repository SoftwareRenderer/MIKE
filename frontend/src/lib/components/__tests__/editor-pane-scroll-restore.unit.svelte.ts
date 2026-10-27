// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, cleanup, act } from '@testing-library/svelte';

vi.mock('svelte', async (importOriginal) => {
    const svelte = await importOriginal<typeof import('svelte')>();
    return {
        ...svelte,
        onMount: (cb: () => void | Promise<void>) => { void Promise.resolve().then(cb); },
    };
});

const readFsFile = vi.fn();

vi.mock('$lib/api', async (importOriginal) => {
    const real = await importOriginal<typeof import('$lib/api')>();
    return {
        ...real,
        readFsFile: (params: { path: string }) => readFsFile(params),
        writeFsFile: () => Promise.resolve(),
    };
});

vi.mock('$lib/stores', () => ({
    appStore: { config: {} },
    dialogStore: { showToast: () => {} },
}));

vi.mock('$lib/storage', () => ({
    getEditorDraft: () => Promise.resolve(null),
    setEditorDraft: () => Promise.resolve(),
    clearEditorDraft: () => Promise.resolve(),
    loadEditorHistory: () => Promise.resolve(null),
    saveEditorHistory: () => Promise.resolve(),
    loadAppSetting: () => Promise.resolve(false),
    saveAppSetting: () => Promise.resolve(),
}));

vi.mock('$lib/utils/lsp-connection-manager', () => ({
    isLSPAvailable: () => false,
    requestLSPChannel: () => ({ release: () => {} }),
}));

import EditorScrollProbe from './EditorScrollProbe.svelte';

const PATH = '/repo/long.txt';
const BODY = Array.from({ length: 400 }, (_, i) => `long line ${i + 1}`).join('\n');

interface Geometry {
    clientHeight: number;
    scrollHeight: number;
    scrollTop: number;
}

/**
 * The geometry jsdom cannot give a pane, which is a 600px window on an 8000px document. `scrollTop` clamps
 * the way a browser clamps it, so an offset lost for want of height is lost here the way it is lost there.
 */
function giveGeometry(el: HTMLElement, state: Geometry) {
    Object.defineProperty(el, 'clientHeight', { configurable: true, get: () => state.clientHeight });
    Object.defineProperty(el, 'scrollHeight', { configurable: true, get: () => state.scrollHeight });
    Object.defineProperty(el, 'scrollTop', {
        configurable: true,
        get: () => state.scrollTop,
        set: (value: number) => {
            state.scrollTop = Math.max(0, Math.min(value, state.scrollHeight - state.clientHeight));
        },
    });
}

const measurable = (): Geometry => ({ clientHeight: 600, scrollHeight: 8000, scrollTop: 0 });

function useFrameClock() {
    vi.useFakeTimers({
        toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'Date',
            'requestAnimationFrame', 'cancelAnimationFrame'],
    });
}

async function settle(ms = 200) {
    await act(async () => {
        await vi.advanceTimersByTimeAsync(ms);
    });
}

/** `scroller()` answers after the load. The pane's element belongs to the view, and the view arrives a frame
 * or two after the mount. */
async function openPane(): Promise<{ scroller: () => HTMLElement | null; setTop: (top: number) => void }> {
    let setTop: (top: number) => void = () => {};
    const { container } = render(EditorScrollProbe, {
        props: {
            projectId: 'p1',
            projectPath: '/repo',
            filePath: PATH,
            register: (set: (top: number) => void) => { setTop = set; },
        },
    });
    await settle();
    expect(container.querySelector('.cm-content')).toBeTruthy();
    return {
        scroller: () => container.querySelector<HTMLElement>('.cm-scroller'),
        setTop: (top) => { void act(async () => { setTop(top); }); },
    };
}

beforeEach(() => {
    useFrameClock();
    readFsFile.mockReset().mockImplementation(({ path }: { path: string }) =>
        Promise.resolve({ content: path === PATH ? BODY : '' }));
});

afterEach(() => {
    vi.useRealTimers();
    cleanup();
});

describe('the offset a reopened editor pane lands on', () => {
    it('applies the offset the pane learns about after it was built', async () => {
        const { scroller, setTop } = await openPane();
        const el = scroller()!;
        const geom = measurable();
        giveGeometry(el, geom);

        setTop(2000);
        await settle();

        expect(geom.scrollTop).toBe(2000);
    });

});
