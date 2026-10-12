/** A `role="button"` row answers Enter and Space; Space would otherwise scroll the panel behind it. */
export function onActivate(run: () => void): (e: KeyboardEvent) => void {
    return (e) => {
        if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault();
            run();
        }
    };
}

/** Each attribute stops one keyboard habit: the red squiggle, mobile capitalisation and word
 *  suggestions, desktop autofill, and a commit key labelled as something else. */
export const LITERAL_TEXT_FIELD = {
    spellcheck: 'false',
    autocomplete: 'off',
    autocorrect: 'off',
    autocapitalize: 'none',
    enterkeyhint: 'done',
} as const;

/**
 * The add row is also the editor of the row clicked from, and it sits under the list — so opening a
 * row brings it into view. Escape is absent on purpose: the modal's focus trap owns it and closes.
 */
export function editInPlace(a: {
    /** Read when the edit starts, not captured at construction. */
    form: () => HTMLElement | null;
    edit: (index: number) => void;
    add: () => void;
}): {
    start: (index: number) => void;
    onFieldKeydown: (e: KeyboardEvent) => void;
} {
    return {
        start(index) {
            a.edit(index);
            requestAnimationFrame(() => a.form()?.scrollIntoView({ block: 'nearest' }));
        },
        onFieldKeydown(e) {
            if (e.key === 'Enter') a.add();
        },
    };
}
