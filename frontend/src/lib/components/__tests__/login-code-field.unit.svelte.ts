import { describe, it, expect, afterEach, beforeEach } from 'vitest';
import { render, cleanup } from '@testing-library/svelte';

import LoginModal from '$lib/components/LoginModal.svelte';
import { resetToEnglish } from '$lib/i18n';


/**
 * The pairing-code field as a person types into it: the text the box holds, and where the caret is once
 * the keystroke is over. The shape belongs to `$lib/utils/login-flow.ts`, and this file pins the half that
 * only exists at a component — the caret survives a keystroke only if the field is written and re-selected
 * inside the same handler, before the browser paints what it did with the caret on its own.
 */

/** The gate opened on a fresh install, whose only body is the code field. */
function codeField(): HTMLInputElement {
    const { container } = render(LoginModal, { hasPasskey: false, onClose: () => { } });
    const el = container.querySelector('input[type="text"]') as HTMLInputElement;
    el.focus();
    return el;
}

/**
 * One keystroke. The DOM already holds `text` with the caret at `caret`, which is what the browser leaves
 * behind after applying the key, and the event says which kind of key it was.
 */
function press(el: HTMLInputElement, text: string, caret: number, inputType = 'insertText'): HTMLInputElement {
    el.value = text;
    el.setSelectionRange(caret, caret);
    el.dispatchEvent(new InputEvent('input', { bubbles: true, inputType }));
    return el;
}

beforeEach(() => {
    resetToEnglish();
    // The code field is only the first body on a browser that can run a ceremony, so the typing tests
    // stand up that surface rather than landing on the explanatory body.
    Object.defineProperty(window, 'isSecureContext', { value: true, configurable: true });
    Object.defineProperty(window, 'PublicKeyCredential', { value: function () { }, configurable: true });
    Object.defineProperty(navigator, 'credentials', {
        value: { create: async () => null, get: async () => null },
        configurable: true,
    });
});
afterEach(cleanup);

describe('typing a pairing code', () => {
    it('breaks the code into two groups without leaving the caret behind', () => {
        const el = codeField();
        press(el, 'A', 1);
        press(el, 'AB', 2);
        press(el, 'ABC', 3);
        press(el, 'ABCD', 4);
        expect(el.value).toBe('ABCD');
        expect(press(el, 'ABCDE', 5).value).toBe('ABCD-E');
        expect(el.selectionStart).toBe(6);
        expect(press(el, 'ABCD-EF', 7).value).toBe('ABCD-EF');
        expect(el.selectionStart).toBe(7);
    });

    it('holds the code a paste brought in whatever shape it had', () => {
        const el = press(codeField(), ' abcd-2345 ', 11);
        expect(el.value).toBe('ABCD-2345');
        expect(el.selectionStart).toBe(9);
    });

    it('spends a backspace on a character when it would only have removed the break', () => {
        const el = press(codeField(), 'ABCDE', 5);
        // The caret sat after the break, so the browser ate the break and left `ABCDE` behind.
        expect(press(el, 'ABCDE', 4, 'deleteContentBackward').value).toBe('ABCE');
        expect(el.selectionStart).toBe(3);
    });

    it('keeps what is typed where the person is looking', () => {
        const el = press(codeField(), 'ABCD-EF', 7);
        expect(press(el, 'ABXCD-EF', 3).value).toBe('ABXC-DEF');
        expect(el.selectionStart).toBe(3);
    });
});

/**
 * The name the pairing body opens with when the page was not opened from a pairing link: what the server
 * read off this browser, and a blank is an answer, so the field invents no placeholder. The link's own
 * name belongs to the login spec, because this harness stubs `onMount`.
 */
describe('the name the pairing body opens with', () => {
    /** The device-name field of a gate opened on a fresh install, whose first body is the pairing form. */
    function nameField(props: Record<string, unknown> = {}): HTMLInputElement {
        const { container } = render(LoginModal, { hasPasskey: false, onClose: () => { }, ...props });
        return container.querySelector('#login-device-name') as HTMLInputElement;
    }

    it('starts with the name the server read off this browser', () => {
        expect(nameField({ deviceName: 'Chrome 120 · Macintosh' }).value).toBe('Chrome 120 · Macintosh');
    });

    it('starts blank when the server read nothing', () => {
        expect(nameField().value).toBe('');
    });

    it('is capped at the length a device label is', () => {
        // The same 64 the server refuses.
        expect(nameField().maxLength).toBe(64);
    });
});
