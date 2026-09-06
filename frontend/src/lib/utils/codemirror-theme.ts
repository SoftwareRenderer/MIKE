import { EditorView } from '@codemirror/view';
import { syntaxHighlighting } from '@codemirror/language';
import { buildHighlightStyle } from '$lib/syntax/token-style';

/** The editor's token colors, straight off the one table (`syntax/token-style.ts`). */
const darkHighlight = buildHighlightStyle('dark');

export const darkTheme = EditorView.theme({
    '&': { backgroundColor: 'transparent', color: '#C9D1D9', height: '100%' },
    '&.cm-focused': { outline: 'none' },
    '.cm-scroller': {
        fontFamily: 'var(--font-mono)',
        fontSize: '14px',
        lineHeight: '1.5',
        tabSize: 4,
        overflow: 'auto',
        position: 'relative',
    },
    '.cm-gutters': {
        backgroundColor: 'var(--color-surface-1)',
        borderRight: '1px solid var(--color-border)',
        color: '#484F58',
        padding: '0 6px 0 0',
    },
    '.cm-activeLine': { backgroundColor: 'rgba(255,255,255,0.04)' },
    '.cm-activeLineGutter': {
        color: '#8B949E',
        background: 'rgba(255,255,255,0.06)',
    },
    // Gutter line selection (click/drag line numbers to create a task)
    '.cm-lineSelGutter': {
        background: 'rgba(88,166,255,0.28)',
        color: '#C9D1D9',
    },
    // Inner lines of a multi-line selection: a horizontally centered vertical
    // connector replaces the (hidden) line number; on hover it yields to the
    // chat bubble (FileEditor.svelte line-sel CSS). Scoped to the git gutter
    // — gutterLineClass applies the class in EVERY gutter (e.g. the fold
    // gutter would render a duplicate line).
    '.cm-gutter.cm-gitGutter .cm-gutterElement.cm-lineSelGutterInner': { position: 'relative' },
    '.cm-gutter.cm-gitGutter .cm-gutterElement.cm-lineSelGutterInner::before': {
        content: '""',
        position: 'absolute',
        left: '50%',
        top: '0',
        bottom: '0',
        width: '2px',
        transform: 'translateX(-50%)',
        background: '#58A6FF',
    },
    '.cm-gutter.cm-gitGutter .cm-gutterElement.cm-lineSelGutterInner:hover::before': {
        content: 'none',
    },
    '.cm-lineSel': { backgroundColor: 'rgba(88,166,255,0.14)' },
    '.cm-matchingBracket': { backgroundColor: 'rgba(88,166,255,0.3)' },
    // Bracket pair colorization (syntax/bracket-pairs.ts): a pair's color is its nesting level, so an
    // opener always matches the closer it opened. VS Code's defaults, cycled over three slots.
    '.cm-bracket-l0': { color: '#FFD700' },
    '.cm-bracket-l1': { color: '#DA70D6' },
    '.cm-bracket-l2': { color: '#179FFF' },
    '.cm-cursor': { borderLeftColor: '#C9D1D9' },
    '.cm-dropCursor': { borderLeftColor: '#C9D1D9' },
    '.cm-searchMatch': { backgroundColor: 'rgba(232,164,20,0.25)', outline: '1px solid rgba(232,164,20,0.5)' },
    '.cm-searchMatch.cm-searchMatch-selected': { backgroundColor: 'rgba(232,164,20,0.4)' },
    '.cm-highlight': { backgroundColor: 'rgba(232,164,20,0.2)' },
    '.cm-diagnostic': { borderLeftColor: '#F97583' },
    '.cm-panel': { border: '1px solid var(--color-border)', color: '#C9D1D9' },
    '.cm-tooltip': {
        border: '1px solid var(--color-border)',
        backgroundColor: 'var(--color-surface-2)',
        color: '#C9D1D9',
        maxWidth: '480px',
        maxHeight: '240px',
        overflow: 'auto',
        padding: '6px 10px',
        fontSize: '13px',
        lineHeight: '1.5',
        borderRadius: '4px',
        tabSize: 4,
    },
    '.cm-tooltip hr': { display: 'none' },
    '.cm-panels': { border: '1px solid var(--color-border)', backgroundColor: 'var(--color-surface-1)', color: '#C9D1D9' },
    '.cm-panels.cm-panels-top': { borderBottom: '1px solid var(--color-border)' },
    '.cm-panels.cm-panels-bottom': { borderTop: '1px solid var(--color-border)' },
    '.cm-foldPlaceholder': {
        backgroundColor: 'rgba(88,166,255,0.3)',
        borderColor: 'rgba(88,166,255,0.4)',
        color: '#C9D1D9',
    },
    '.cm-search': { color: '#C9D1D9' },
    '.cm-search input': {
        backgroundColor: 'var(--color-surface-3)',
        color: '#C9D1D9',
        border: '1px solid var(--color-border)',
        borderRadius: '4px',
        padding: '2px 6px',
        fontSize: '12px',
        fontFamily: 'inherit',
    },
    '.cm-search input:focus': { outline: '1px solid var(--color-accent)' },
    '.cm-search button': {
        color: '#C9D1D9',
        backgroundColor: 'var(--color-surface-3)',
        border: '1px solid var(--color-border)',
        borderRadius: '4px',
        cursor: 'pointer',
        fontSize: '12px',
        fontFamily: 'inherit',
        padding: '1px 6px',
    },
    '.cm-search button:hover': { backgroundColor: 'var(--color-surface-4)' },
    '.cm-indent-guide': { borderLeftColor: '#21252B' },
    '.cm-tooltip-autocomplete': {
        whiteSpace: 'nowrap',
        backgroundColor: 'var(--color-surface-2)',
        border: '1px solid var(--color-border)',
        borderRadius: '4px',
        fontSize: '12px',
    },
    '.cm-completionInfo': {
        paddingLeft: '6px',
        paddingRight: '6px',
        whiteSpace: 'normal',
        color: '#C9D1D9',
    },
    '.cm-completionIcon': {
        color: '#C9D1D9',
    },
    '.cm-completionIconWarning': {
        color: '#D29922',
    },
    '.cm-completionIconInfo': {
        color: '#58A6FF',
    },
    '.cm-sticky-scroll': {
        position: 'absolute',
        top: '0',
        left: '0',
        zIndex: '5',
        display: 'none',
        backgroundColor: 'rgba(30, 30, 46, 0.95)',
        borderBottom: '1px solid var(--color-border)',
        pointerEvents: 'none',
        overflow: 'hidden',
    },
    '.cm-sticky-scroll-row': {
        padding: '2px 10px 2px 6px',
        fontFamily: 'var(--font-mono)',
        fontSize: '14px',
        lineHeight: '1.5',
        color: '#C9D1D9',
        whiteSpace: 'pre',
        overflow: 'hidden',
        textOverflow: 'ellipsis',
        borderBottom: '1px solid rgba(128, 128, 128, 0.28)',
    },
    '.cm-sticky-scroll-row:last-child': {
        borderBottom: 'none',
    },
});

export const darkSyntax = syntaxHighlighting(darkHighlight);

// --- Light theme ---

const lightHighlight = buildHighlightStyle('light');

export const lightTheme = EditorView.theme({
    '&': { backgroundColor: 'transparent', color: '#24292F', height: '100%' },
    '&.cm-focused': { outline: 'none' },
    '.cm-scroller': {
        fontFamily: 'var(--font-mono)',
        fontSize: '14px',
        lineHeight: '1.5',
        tabSize: 4,
        overflow: 'auto',
        position: 'relative',
    },
    '.cm-gutters': {
        backgroundColor: 'var(--color-surface-1)',
        borderRight: '1px solid var(--color-border)',
        color: '#B9BABC',
        padding: '0 6px 0 0',
    },
    '.cm-activeLine': { backgroundColor: 'rgba(0,0,0,0.04)' },
    '.cm-activeLineGutter': {
        color: '#6E7781',
        background: 'rgba(0,0,0,0.06)',
    },
    // Gutter line selection (click/drag line numbers to create a task)
    '.cm-lineSelGutter': {
        background: 'rgba(9,105,218,0.25)',
        color: '#24292F',
    },
    // Inner lines of a multi-line selection: a horizontally centered vertical
    // connector replaces the (hidden) line number; on hover it yields to the
    // chat bubble (FileEditor.svelte line-sel CSS). Scoped to the git gutter
    // — gutterLineClass applies the class in EVERY gutter (e.g. the fold
    // gutter would render a duplicate line).
    '.cm-gutter.cm-gitGutter .cm-gutterElement.cm-lineSelGutterInner': { position: 'relative' },
    '.cm-gutter.cm-gitGutter .cm-gutterElement.cm-lineSelGutterInner::before': {
        content: '""',
        position: 'absolute',
        left: '50%',
        top: '0',
        bottom: '0',
        width: '2px',
        transform: 'translateX(-50%)',
        background: '#0969DA',
    },
    '.cm-gutter.cm-gitGutter .cm-gutterElement.cm-lineSelGutterInner:hover::before': {
        content: 'none',
    },
    '.cm-lineSel': { backgroundColor: 'rgba(9,105,218,0.12)' },
    '.cm-matchingBracket': { backgroundColor: 'rgba(9,105,218,0.2)' },
    // Same three levels as the dark theme, at the weights VS Code uses on a light background.
    '.cm-bracket-l0': { color: '#795E26' },
    '.cm-bracket-l1': { color: '#AF00DB' },
    '.cm-bracket-l2': { color: '#001080' },
    '.cm-cursor': { borderLeftColor: '#24292F' },
    '.cm-dropCursor': { borderLeftColor: '#24292F' },
    '.cm-searchMatch': { backgroundColor: 'rgba(220,160,0,0.25)', outline: '1px solid rgba(220,160,0,0.5)' },
    '.cm-searchMatch.cm-searchMatch-selected': { backgroundColor: 'rgba(220,160,0,0.4)' },
    '.cm-highlight': { backgroundColor: 'rgba(220,160,0,0.2)' },
    '.cm-diagnostic': { borderLeftColor: '#CF222E' },
    '.cm-panel': { border: '1px solid var(--color-border)', color: '#24292F' },
    '.cm-tooltip': {
        border: '1px solid var(--color-border)',
        backgroundColor: 'var(--color-surface-2)',
        color: '#24292F',
        maxWidth: '480px',
        maxHeight: '240px',
        overflow: 'auto',
        padding: '6px 10px',
        fontSize: '13px',
        lineHeight: '1.5',
        borderRadius: '4px',
        tabSize: 4,
    },
    // See the dark theme: a language server's `---` divider renders no line.
    '.cm-tooltip hr': { display: 'none' },
    '.cm-panels': { border: '1px solid var(--color-border)', backgroundColor: 'var(--color-surface-1)', color: '#24292F' },
    '.cm-panels.cm-panels-top': { borderBottom: '1px solid var(--color-border)' },
    '.cm-panels.cm-panels-bottom': { borderTop: '1px solid var(--color-border)' },
    '.cm-foldPlaceholder': {
        backgroundColor: 'rgba(9,105,218,0.15)',
        borderColor: 'rgba(9,105,218,0.3)',
        color: '#24292F',
    },
    '.cm-search': { color: '#24292F' },
    '.cm-search input': {
        backgroundColor: 'var(--color-surface-3)',
        color: '#24292F',
        border: '1px solid var(--color-border)',
        borderRadius: '4px',
        padding: '2px 6px',
        fontSize: '12px',
        fontFamily: 'inherit',
    },
    '.cm-search input:focus': { outline: '1px solid var(--color-accent)' },
    '.cm-search button': {
        color: '#24292F',
        backgroundColor: 'var(--color-surface-3)',
        border: '1px solid var(--color-border)',
        borderRadius: '4px',
        cursor: 'pointer',
        fontSize: '12px',
        fontFamily: 'inherit',
        padding: '1px 6px',
    },
    '.cm-search button:hover': { backgroundColor: 'var(--color-surface-4)' },
    '.cm-indent-guide': { borderLeftColor: '#E8E8E8' },
    '.cm-tooltip-autocomplete': {
        whiteSpace: 'nowrap',
        backgroundColor: 'var(--color-surface-2)',
        border: '1px solid var(--color-border)',
        borderRadius: '4px',
        fontSize: '12px',
    },
    '.cm-completionInfo': {
        paddingLeft: '6px',
        paddingRight: '6px',
        whiteSpace: 'normal',
        color: '#24292F',
    },
    '.cm-completionIcon': {
        color: '#24292F',
    },
    '.cm-completionIconWarning': {
        color: '#9A6700',
    },
    '.cm-completionIconInfo': {
        color: '#0969DA',
    },
    '.cm-sticky-scroll': {
        position: 'absolute',
        top: '0',
        left: '0',
        zIndex: '5',
        display: 'none',
        backgroundColor: 'rgba(255, 255, 255, 0.95)',
        borderBottom: '1px solid var(--color-border)',
        pointerEvents: 'none',
        overflow: 'hidden',
    },
    '.cm-sticky-scroll-row': {
        padding: '2px 10px 2px 6px',
        fontFamily: 'var(--font-mono)',
        fontSize: '14px',
        lineHeight: '1.5',
        color: '#24292F',
        whiteSpace: 'pre',
        overflow: 'hidden',
        textOverflow: 'ellipsis',
        borderBottom: '1px solid rgba(128, 128, 128, 0.28)',
    },
    '.cm-sticky-scroll-row:last-child': {
        borderBottom: 'none',
    },
});

export const lightSyntax = syntaxHighlighting(lightHighlight);

/**
 * Block spacing for the markdown a language server sends into a tooltip — the
 * hover card, completion info and signature help all render it, and all three
 * carry the `.cm-lsp-documentation` class the renderer puts on their content
 * element (the hover card's docs div carries it too).
 *
 * A server's documentation is a signature block followed by prose, and the
 * browser lays that out as if it were a page: 1em margins around paragraphs,
 * lists and headings, plus `1.5em`+ font sizes on the headings `####` doc
 * comments produce. In a 12px tooltip that reads as mostly whitespace, so every
 * block element is pulled to the 2px the renderer itself uses for `p`/`pre`,
 * with the first and last block flush against the card edge.
 *
 * Color-free on purpose — this belongs to neither theme, so it is not
 * duplicated in both.
 */
const LSP_DOCS = '.cm-lsp-documentation';
const HEADINGS = ['h1', 'h2', 'h3', 'h4', 'h5', 'h6'].map(h => `${LSP_DOCS} ${h}`).join(', ');

export const lspDocsTheme = EditorView.baseTheme({
    [`${LSP_DOCS} p`]: { margin: '2px 0' },
    // A heading in documentation is a label over the block beneath it, not a
    // page header: the card's own size, one step of separation above.
    [HEADINGS]: { fontSize: '1em', fontWeight: 600, lineHeight: '1.3', margin: '3px 0 1px' },
    // The default 40px list indent is a third of a narrow tooltip.
    [`${LSP_DOCS} ul, ${LSP_DOCS} ol`]: { margin: '2px 0', paddingLeft: '1.2em' },
    [`${LSP_DOCS} li`]: { margin: '1px 0' },
    [`${LSP_DOCS} blockquote`]: { margin: '2px 0', paddingLeft: '6px' },
    [`${LSP_DOCS} > :first-child`]: { marginTop: '0' },
    [`${LSP_DOCS} > :last-child`]: { marginBottom: '0' },
});

/**
 * Detect whether a light theme is active by checking a CSS variable.
 * Theme extensions override --color-bg-page to a light value.
 * We check if the background is lighter than a midpoint.
 */
export function isLightTheme(): boolean {
    try {
        const bg = getComputedStyle(document.documentElement)
            .getPropertyValue('--color-bg-page')
            .trim();
        if (!bg) return false;
        // Parse hex color and check brightness
        const r = parseInt(bg.slice(1, 3), 16);
        const g = parseInt(bg.slice(3, 5), 16);
        const b = parseInt(bg.slice(5, 7), 16);
        const brightness = (r * 299 + g * 587 + b * 114) / 1000;
        return brightness > 128;
    } catch {
        return false;
    }
}
