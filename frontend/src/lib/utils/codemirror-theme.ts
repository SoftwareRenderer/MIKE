import { EditorView } from '@codemirror/view';
import { HighlightStyle, syntaxHighlighting } from '@codemirror/language';
import { tags } from '@lezer/highlight';

export const darkHighlight = HighlightStyle.define([
    { tag: tags.keyword, color: '#F97583' },
    { tag: tags.constant(tags.keyword), color: '#79C0FF' },
    { tag: tags.definition(tags.variableName), color: '#FFA657' },
    { tag: tags.variableName, color: '#FFA657' },
    { tag: tags.function(tags.variableName), color: '#D2A8FF' },
    { tag: tags.propertyName, color: '#79C0FF' },
    { tag: tags.string, color: '#A5D6FF' },
    { tag: tags.number, color: '#79C0FF' },
    { tag: tags.comment, color: '#8B949E', fontStyle: 'italic' },
    { tag: tags.operator, color: '#FF7B72' },
    { tag: tags.punctuation, color: '#C9D1D9' },
    { tag: tags.className, color: '#FFA657' },
    { tag: tags.typeName, color: '#FFA657' },
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
    '.cm-selectionBackground': { backgroundColor: 'rgba(88,166,255,0.3)' },
    '&.cm-focused .cm-selectionBackground': { backgroundColor: 'rgba(88,166,255,0.3)' },
    '.cm-cursor': { borderLeftColor: '#C9D1D9' },
    '.cm-dropCursor': { borderLeftColor: '#C9D1D9' },
    '.cm-searchMatch': { backgroundColor: 'rgba(232,164,20,0.25)', outline: '1px solid rgba(232,164,20,0.5)' },
    '.cm-searchMatch.cm-searchMatch-selected': { backgroundColor: 'rgba(232,164,20,0.4)' },
    '.cm-highlight': { backgroundColor: 'rgba(232,164,20,0.2)' },
    '.cm-diagnostic': { borderLeftColor: '#F97583' },
    '.cm-panel': { border: '1px solid var(--color-border)', color: '#C9D1D9' },
    '.cm-tooltip': { border: '1px solid var(--color-border)', backgroundColor: 'var(--color-surface-2)', color: '#C9D1D9' },
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
