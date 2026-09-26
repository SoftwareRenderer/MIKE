import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, cleanup } from '@testing-library/svelte';
import { tick } from 'svelte';
import type { DiagramAnswer } from '$lib/utils/diagram';

// A redraw hands the fence the same text again, and what breaks is the frame before the renderer answers.
// The renderer is a stub that has not answered yet, so that frame is what these tests read.
const { drawnDiagram, renderDiagram } = vi.hoisted(() => ({
    drawnDiagram: vi.fn((_source: string): DiagramAnswer | null => null),
    renderDiagram: vi.fn((_source: string): Promise<DiagramAnswer> => new Promise(() => {})),
}));
vi.mock('$lib/utils/diagram', () => ({ drawnDiagram, renderDiagram }));

import DiagramFence from '$lib/components/Chat/DiagramFence.svelte';

const SOURCE = 'flowchart TD\n  A[Alpha] --> B[Beta]';

const DRAWING: DiagramAnswer = {
    ok: true,
    imageSrc: 'data:image/svg+xml;base64,PHN2Zy8+',
    markup: '<svg xmlns="http://www.w3.org/2000/svg"></svg>',
};

const view = (container: HTMLElement) => ({
    image: container.querySelector<HTMLImageElement>('img.diagram'),
    source: container.querySelector('.diagram-fence pre'),
    note: container.querySelector('.diagram-fence .note'),
});

beforeEach(() => {
    drawnDiagram.mockReset().mockImplementation(() => null);
    renderDiagram.mockReset().mockImplementation(() => new Promise(() => {}));
});

afterEach(cleanup);

describe('a diagram fence handed text again', () => {
    it('shows the drawing that text already produced, without asking the renderer', async () => {
        drawnDiagram.mockReturnValue(DRAWING);
        const { container } = render(DiagramFence, { text: SOURCE });
        await tick();
        const seen = view(container);
        expect(seen.image?.getAttribute('src')).toBe(DRAWING.imageSrc);
        expect(seen.source).toBeNull();
        expect(renderDiagram).not.toHaveBeenCalled();
    });

    it('shows the text it came from while the first drawing is asked for', async () => {
        const { container } = render(DiagramFence, { text: SOURCE });
        await tick();
        const seen = view(container);
        expect(seen.image).toBeNull();
        expect(seen.source?.textContent).toContain('flowchart TD');
        expect(seen.note?.textContent).toContain('Rendering');
        expect(renderDiagram).toHaveBeenCalledWith(SOURCE);
    });

    it('looks at what it has drawn before asking for text it has never drawn', async () => {
        const { rerender } = render(DiagramFence, { text: SOURCE });
        drawnDiagram.mockClear();
        renderDiagram.mockClear();
        await rerender({ text: 'flowchart LR\n  A[Other] --> B[Text]' });
        await tick();
        expect(drawnDiagram).toHaveBeenCalledWith('flowchart LR\n  A[Other] --> B[Text]');
        expect(renderDiagram).toHaveBeenCalledWith('flowchart LR\n  A[Other] --> B[Text]');
    });
});
