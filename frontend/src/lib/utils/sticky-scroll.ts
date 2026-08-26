import { ViewPlugin, ViewUpdate, EditorView } from '@codemirror/view';
import { StateEffect, StateField } from '@codemirror/state';

/** Section header for sticky scroll. */
export interface SectionHeader {
    line: number;
    endLine: number; // last line that belongs to this section
}

/** Effect to update section headers from the parent component. */
export const updateHeadersEffect = StateEffect.define<SectionHeader[]>();

/** StateField that reacts to updateHeadersEffect. */
export const sectionHeadersField = StateField.define<SectionHeader[]>({
    create() { return []; },
    update(value, tr) {
        for (const e of tr.effects) {
            if (e.is(updateHeadersEffect)) value = e.value;
        }
        return value;
    },
});

/**
 * The bar detaches this many lines before the end of the current section, so
 * it does not cover the upcoming section header as you scroll toward it.
 */
const HEADER_DETACH_MARGIN = 3;

/**
 * Find the section header that should be sticky at the current scroll position
 */
export function findStickyHeader(
    view: EditorView,
    headers: SectionHeader[],
): SectionHeader | undefined {
    if (headers.length === 0) return undefined;

    const topLine = view.state.doc.lineAt(view.viewport.from).number;

    let result: SectionHeader | undefined;
    for (const header of headers) {
        if (header.line <= topLine && topLine < header.endLine - HEADER_DETACH_MARGIN) {
            result = header;
        } else if (header.line > topLine) {
            break;
        }
    }

    return result;
}

/**
 * Get the gutter width in pixels.
 */
function getGutterWidth(editor: HTMLElement): number {
    const gutters = editor.querySelector('.cm-gutters') as HTMLElement | null;
    return gutters ? gutters.offsetWidth : 0;
}

/**
 * Get the scrollbar width in pixels.
 */
function getScrollbarWidth(editor: HTMLElement): number {
    const scroller = editor.querySelector('.cm-scroller') as HTMLElement | null;
    return scroller ? scroller.offsetWidth - scroller.clientWidth : 0;
}

/**
 * Walk up from a DOM node to the CodeMirror line element
 */
export function findLineElement(start: Node | null): Element | null {
    let el: Node | null = start;
    let depth = 0;
    while (el && depth < 30) {
        if (el.nodeType === 1 && (el as Element).classList.contains('cm-line')) {
            return el as Element;
        }
        el = el.parentNode;
        depth++;
    }
    return null;
}

/**
 * Get the highlighted HTML for a declaration line by cloning from the
 * editor's DOM. Falls back to plain text if the line is not in viewport
 * (or no line element is reachable from the anchor node).
 */
export function getHeaderLineHtml(view: EditorView, header: SectionHeader): string {
    const line = view.state.doc.line(header.line);
    const plain = line.text.trim();
    // Preserve leading indentation
    const indent = line.text.match(/^(\s*)/)?.[1] ?? '';

    // Try to find the line's DOM in the visible viewport
    const lineStart = line.from;
    const dom = view.domAtPos(lineStart);
    const lineEl = findLineElement(dom?.node ?? null);
    if (lineEl) {
        // Clone the highlighted line content (has inline styles from defaultHighlightStyle)
        const clone = lineEl.cloneNode(true) as Element;
        clone.removeAttribute('class');
        clone.removeAttribute('style');
        clone.removeAttribute('spellcheck');
        const inner = clone.innerHTML;
        if (inner && inner.length > plain.length) {
            return inner;
        }
    }

    return escapeHtml(indent) + escapeHtml(plain);
}

/** Escape HTML special characters. */
function escapeHtml(text: string): string {
    return text.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

/**
 * Create a sticky scroll ViewPlugin.
 *
 * Headers are provided by the parent component via `updateHeadersEffect`.
 * The plugin reads them from `sectionHeadersField` in the editor state.
 *
 * When scrolling through code, the current section header (function/class
 * declaration) sticks to the top of the editor until scrolling into the
 * next section.
 */
export function stickyScroll() {
    return ViewPlugin.fromClass(
        class {
            element: HTMLElement;
            currentHeader: SectionHeader | null = null;
            // Cache of what the bar last rendered: the header line number and
            // its plain text at render time. updateSticky() re-renders when
            // either (or the header's endLine) changes, so an in-place edit of
            // the header line (e.g. a rename — same line number) is picked up.
            renderedLine = 0;
            renderedLineText = '';

            constructor(view: EditorView) {
                this.element = document.createElement('div');
                this.element.className = 'cm-sticky-scroll';
                this.element.setAttribute('aria-hidden', 'true');

                const insert = () => {
                    if (view.dom.parentElement && !this.element.parentElement) {
                        view.dom.appendChild(this.element);
                        this.setupScrollListener(view);
                        this.updateSticky(view);
                    }
                };

                if (view.dom.parentElement) {
                    insert();
                } else {
                    const observer = new MutationObserver(() => {
                        if (view.dom.parentElement) {
                            observer.disconnect();
                            insert();
                        }
                    });
                    observer.observe(document.body, { childList: true, subtree: true });
                    setTimeout(() => observer.disconnect(), 500);
                }
            }

            setupScrollListener(view: EditorView) {
                const scroller = view.dom.querySelector('.cm-scroller') as HTMLElement | null;
                if (!scroller) return;

                const onScroll = () => {
                    this.updateSticky(view);
                };

                scroller.addEventListener('scroll', onScroll, { passive: true });
                (this as any)._scrollHandler = onScroll;
                (this as any)._scroller = scroller;
            }

            update(update: ViewUpdate) {
                this.updateSticky(update.view);
            }

            updateSticky(view: EditorView) {
                const headers = view.state.field(sectionHeadersField, false) || [];
                if (headers.length === 0) {
                    this.hide();
                    return;
                }

                // Drop headers whose line is outside the document —
                // getHeaderLineHtml calls doc.line(n), which throws RangeError
                // for out-of-range n (e.g. a malformed LSP symbol with line: 0)
                // and would otherwise crash the scroll/view update handler.
                const lineCount = view.state.doc.lines;
                const validHeaders = headers.filter((h) => h.line >= 1 && h.line <= lineCount);
                if (validHeaders.length === 0) {
                    this.hide();
                    return;
                }

                const header = findStickyHeader(view, validHeaders);

                if (header) {
                    // Re-render when the sticky section changed, its extent
                    // changed, or the header line's own text changed (in-place
                    // edit at the same line number). The DOM clone inside
                    // getHeaderLineHtml picks up fresh syntax highlighting.
                    const lineText = view.state.doc.line(header.line).text;
                    if (
                        this.renderedLine !== header.line ||
                        !this.currentHeader ||
                        this.currentHeader.endLine !== header.endLine ||
                        this.renderedLineText !== lineText
                    ) {
                        this.currentHeader = header;
                        this.renderedLine = header.line;
                        this.renderedLineText = lineText;
                        const html = getHeaderLineHtml(view, header);
                        this.element.innerHTML = html;
                        const gutterW = getGutterWidth(view.dom);
                        const scrollbarW = getScrollbarWidth(view.dom);
                        this.element.style.left = gutterW + 'px';
                        this.element.style.width = `calc(100% - ${gutterW}px - ${scrollbarW}px)`;
                        this.element.style.display = 'block';
                    }
                } else if (this.currentHeader) {
                    // Reset the render cache so re-entering this section later
                    // (same line, same text) re-renders instead of staying hidden.
                    this.currentHeader = null;
                    this.renderedLine = 0;
                    this.renderedLineText = '';
                    this.hide();
                }
            }

            hide() {
                this.element.style.display = 'none';
            }

            destroy() {
                const scroller = (this as any)._scroller as HTMLElement | null;
                const handler = (this as any)._scrollHandler as (() => void) | null;
                if (scroller && handler) {
                    scroller.removeEventListener('scroll', handler);
                }
                this.element.remove();
            }
        },
        {},
    );
}
