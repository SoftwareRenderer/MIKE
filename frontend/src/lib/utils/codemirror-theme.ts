import { EditorView } from '@codemirror/view';
import { HighlightStyle, syntaxHighlighting } from '@codemirror/language';
import { tags } from '@lezer/highlight';

export const darkHighlight = HighlightStyle.define([
    { tag: tags.keyword, color: '#F97583' },
    { tag: tags.constant(tags.keyword), color: '#79C0FF' },
    { tag: tags.function(tags.variableName), color: '#D2A8FF' },
    { tag: tags.function(tags.definition(tags.variableName)), color: '#D2A8FF' },
    { tag: tags.definition(tags.variableName), color: '#C9D1D9' },
    { tag: tags.variableName, color: '#C9D1D9' },
    { tag: tags.propertyName, color: '#D2A8FF' },
    { tag: tags.string, color: '#A5D6FF' },
    { tag: tags.number, color: '#79C0FF' },
    { tag: tags.comment, color: '#8B949E', fontStyle: 'italic' },
    { tag: tags.operator, color: '#FF7B72' },
    { tag: tags.punctuation, color: '#C9D1D9' },
    { tag: tags.className, color: '#FFA657' },
    { tag: tags.typeName, color: '#39D2C0' },
    { tag: tags.tagName, color: '#7EE787' },
    { tag: tags.attributeName, color: '#79C0FF' },
    { tag: tags.attributeValue, color: '#A5D6FF' },
    { tag: tags.regexp, color: '#A5D6FF' },
    { tag: tags.meta, color: '#C9D1D9' },
    { tag: tags.invalid, color: '#f97583', textDecoration: 'underline wavy #f97583' },
]);

export const darkTheme = EditorView.theme({
    '&': { backgroundColor: 'transparent', color: '#C9D1D9', height: '100%' },
    '&.cm-focused': { outline: 'none' },
    '.cm-scroller': {
        fontFamily: 'var(--font-mono)',
        fontSize: '14px',
        lineHeight: '1.5',
        tabSize: 4,
        overflow: 'auto',
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
    '.cm-matchingBracket': { backgroundColor: 'rgba(88,166,255,0.3)' },
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
    '.cm-tooltip hr': {
        border: 'none',
        borderTop: '1px solid var(--color-border)',
        margin: '6px 0',
    },
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
});

export const darkSyntax = syntaxHighlighting(darkHighlight);

// --- Light theme ---

export const lightHighlight = HighlightStyle.define([
    { tag: tags.keyword, color: '#CF222E' },
    { tag: tags.constant(tags.keyword), color: '#0550AE' },
    { tag: tags.function(tags.variableName), color: '#8250DF' },
    { tag: tags.function(tags.definition(tags.variableName)), color: '#8250DF' },
    { tag: tags.definition(tags.variableName), color: '#24292F' },
    { tag: tags.variableName, color: '#24292F' },
    { tag: tags.propertyName, color: '#8250DF' },
    { tag: tags.string, color:'#0A3069' },
    { tag: tags.number, color: '#0550AE' },
    { tag: tags.comment, color: '#6E7781', fontStyle: 'italic' },
    { tag: tags.operator, color: '#CF222E' },
    { tag: tags.punctuation, color: '#24292F' },
    { tag: tags.className, color: '#953800' },
    { tag: tags.typeName, color: '#0550AE' },
    { tag: tags.tagName, color: '#116329' },
    { tag: tags.attributeName, color: '#0550AE' },
    { tag: tags.attributeValue, color: '#0A3069' },
    { tag: tags.regexp, color: '#0A3069' },
    { tag: tags.meta, color: '#24292F' },
    { tag: tags.invalid, color: '#CF222E', textDecoration: 'underline wavy #CF222E' },
]);

export const lightTheme = EditorView.theme({
    '&': { backgroundColor: 'transparent', color: '#24292F', height: '100%' },
    '&.cm-focused': { outline: 'none' },
    '.cm-scroller': {
        fontFamily: 'var(--font-mono)',
        fontSize: '14px',
        lineHeight: '1.5',
        tabSize: 4,
        overflow: 'auto',
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
    '.cm-matchingBracket': { backgroundColor: 'rgba(9,105,218,0.2)' },
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
    '.cm-tooltip hr': {
        border: 'none',
        borderTop: '1px solid var(--color-border)',
        margin: '6px 0',
    },
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
});

export const lightSyntax = syntaxHighlighting(lightHighlight);

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
