import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, cleanup, fireEvent } from '@testing-library/svelte';
import ColorPicker from '$lib/components/FileEditor/ColorPicker.svelte';
import type { ColorTarget } from '$lib/utils/editor-color-picker';

const SIZE = { width: 248, edge: 8, height: 84, gap: 6 };

function target(over: Partial<ColorTarget> = {}): ColorTarget {
    return { from: 0, to: 4, color: '#f00', left: 100, top: 40, bottom: 58, ...over };
}

function open(over: Partial<ColorTarget> = {}, onApply = vi.fn(), onClose = vi.fn()) {
    const screen = render(ColorPicker, { props: { target: target(over), onApply, onClose } });
    const card = screen.getByRole('dialog');
    const token = card.querySelector<HTMLInputElement>('.token')!;
    const swatch = card.querySelector<HTMLInputElement>('.swatch')!;
    const apply = [...card.querySelectorAll('button')].find((b) => !b.classList.contains('btn-ghost'))!;
    return { ...screen, card, token, swatch, apply, onApply, onClose };
}

/** What the reader types. */
async function type(input: HTMLInputElement, value: string) {
    input.value = value;
    await fireEvent.input(input);
}

afterEach(() => cleanup());

describe('the card a clicked color opens', () => {
    it('opens on the token the reader clicked', () => {
        const opened = open({ color: '#112233AA' });
        expect(opened.token.value).toBe('#112233AA');
        expect(opened.swatch.value).toBe('#112233');
    });

    it('applies the picked color in the token’s own clothes', async () => {
        const opened = open({ color: '#FFF' });
        await type(opened.token, '#a0b1c2');
        await fireEvent.click(opened.apply);
        expect(opened.onApply).toHaveBeenCalledWith('#A0B1C2');
    });

    it('keeps the alpha the field carries', async () => {
        const opened = open({ color: '#112233aa' });
        await type(opened.token, '#00ff0080');
        await fireEvent.click(opened.apply);
        expect(opened.onApply).toHaveBeenCalledWith('#00ff0080');
    });

    it('lets the swatch choose the color and the field keep the spelling', async () => {
        const opened = open({ color: '#FFF' });
        opened.swatch.value = '#a0b1c2';
        await fireEvent.input(opened.swatch);
        expect(opened.token.value).toBe('#A0B1C2');
        await fireEvent.click(opened.apply);
        expect(opened.onApply).toHaveBeenCalledWith('#A0B1C2');
    });

    it('refuses to apply text that is not a color', async () => {
        const opened = open();
        await type(opened.token, 'zazaza');
        expect(opened.token.getAttribute('aria-invalid')).toBe('true');
        expect(opened.apply.disabled).toBe(true);
        await fireEvent.click(opened.apply);
        expect(opened.onApply).not.toHaveBeenCalled();
    });
});

describe('the keyboard', () => {
    it('applies on Enter and closes on Escape, both from inside the card', async () => {
        const opened = open({ color: '#f00' });
        await fireEvent.keyDown(opened.token, { key: 'Enter' });
        expect(opened.onApply).toHaveBeenCalledWith('#f00');
        await fireEvent.keyDown(opened.token, { key: 'Escape' });
        expect(opened.onClose).toHaveBeenCalled();
    });

    it('leaves Enter pressed in the editor behind it alone', async () => {
        const opened = open();
        const editor = document.createElement('div');
        document.body.appendChild(editor);
        await fireEvent.keyDown(editor, { key: 'Enter' });
        expect(opened.onApply).not.toHaveBeenCalled();
    });
});

describe('what closes the card', () => {
    it('closes on a press outside it', async () => {
        const opened = open();
        await fireEvent.pointerDown(document.body);
        expect(opened.onClose).toHaveBeenCalled();
    });

    it('stays open for a press inside it', async () => {
        const opened = open();
        await fireEvent.pointerDown(opened.token);
        expect(opened.onClose).not.toHaveBeenCalled();
    });
});

describe('where the card sits', () => {
    it('hangs below the token when there is room', () => {
        const opened = open({ bottom: 100, top: 80 });
        expect(opened.card.style.top).toBe(`${100 + SIZE.gap}px`);
    });

    it('turns above the token at the bottom of the window', () => {
        const near = { top: window.innerHeight - 20, bottom: window.innerHeight - 4 };
        const opened = open(near);
        expect(parseFloat(opened.card.style.top)).toBeLessThan(near.top);
    });

    it('keeps the whole card inside a window narrower than it is wide', () => {
        const opened = open({ left: window.innerWidth - 10 });
        expect(parseFloat(opened.card.style.left)).toBeLessThanOrEqual(
            window.innerWidth - SIZE.width - SIZE.edge,
        );
    });
});
