import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { createFocusTrap } from '../a11y';

describe('createFocusTrap', () => {
    let container: HTMLElement;
    let windowEscSpy: ReturnType<typeof vi.fn<(e: KeyboardEvent) => void>>;

    beforeEach(() => {
        container = document.createElement('div');
        container.innerHTML = `
            <button id="first">First</button>
            <button id="second">Second</button>
        `;
        document.body.appendChild(container);
        // A window-level Escape listener (like CardDetail's handler that closes
        // the Task View) must NOT fire when a modal trap handles Escape.
        windowEscSpy = vi.fn<(e: KeyboardEvent) => void>();
        window.addEventListener('keydown', windowEscSpy);
    });

    afterEach(() => {
        window.removeEventListener('keydown', windowEscSpy);
        document.body.innerHTML = '';
        vi.restoreAllMocks();
    });

    it('calls onClose when Escape is pressed', () => {
        const onClose = vi.fn();
        const trap = createFocusTrap(container, onClose);
        container.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
        expect(onClose).toHaveBeenCalledTimes(1);
        trap.destroy();
    });

    it('stops Escape from propagating to window-level handlers', () => {
        const onClose = vi.fn();
        const trap = createFocusTrap(container, onClose);
        // Dispatch on the container — bubbles up through document to window.
        container.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
        // The trap closed the modal...
        expect(onClose).toHaveBeenCalledTimes(1);
        // ...but the window listener (e.g. the Task View close handler) never ran.
        expect(windowEscSpy).not.toHaveBeenCalled();
        trap.destroy();
    });

    it('does not stop non-Escape keys from propagating', () => {
        const onClose = vi.fn();
        const trap = createFocusTrap(container, onClose);
        container.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
        expect(onClose).not.toHaveBeenCalled();
        expect(windowEscSpy).toHaveBeenCalledTimes(1); // reached the window listener
        trap.destroy();
    });

    it('focusFirst prefers a later autofocus element over earlier focusables', () => {
        container.innerHTML = `
            <button id="first">First</button>
            <textarea id="auto" autofocus></textarea>
        `;
        const first = document.getElementById('first') as HTMLButtonElement;
        const auto = document.getElementById('auto') as HTMLTextAreaElement;
        // jsdom reports offsetParent as null; fake visibility like the Tab test.
        Object.defineProperty(first, 'offsetParent', { value: {} });
        Object.defineProperty(auto, 'offsetParent', { value: {} });
        const trap = createFocusTrap(container);
        trap.focusFirst();
        expect(document.activeElement).toBe(auto);
        trap.destroy();
    });

    it('focusFirst falls back to the first focusable element when nothing is marked autofocus', () => {
        const first = document.getElementById('first') as HTMLButtonElement;
        const second = document.getElementById('second') as HTMLButtonElement;
        Object.defineProperty(first, 'offsetParent', { value: {} });
        Object.defineProperty(second, 'offsetParent', { value: {} });
        const trap = createFocusTrap(container);
        trap.focusFirst();
        expect(document.activeElement).toBe(first);
        trap.destroy();
    });

    it('wraps Tab focus within the container', () => {
        const trap = createFocusTrap(container);
        const first = document.getElementById('first') as HTMLButtonElement;
        const second = document.getElementById('second') as HTMLButtonElement;
        // jsdom reports offsetParent as null, but the trap treats null as
        // "hidden" — give the buttons a fake offsetParent so they're focusable.
        Object.defineProperty(first, 'offsetParent', { value: {} });
        Object.defineProperty(second, 'offsetParent', { value: {} });
        first.focus();

        // Tab on last element wraps to first
        second.focus();
        second.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true }));
        expect(document.activeElement).toBe(first);

        // Shift+Tab on first element wraps to last
        first.focus();
        first.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', shiftKey: true, bubbles: true }));
        expect(document.activeElement).toBe(second);
        trap.destroy();
    });
});
