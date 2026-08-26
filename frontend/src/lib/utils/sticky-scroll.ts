import { ViewPlugin, ViewUpdate, EditorView } from '@codemirror/view';
import { StateEffect, StateField } from '@codemirror/state';

/** Section header for sticky scroll. */
export interface SectionHeader {
    line: number,
    endLine: number; // last line in section
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
 * The bar detaches few lines before the end of a section to avoid
 * covering the next section header.
 */
const HEADER_DETACH_MARGIN = 3;

/** Find the section header that should be sticky at the current scroll position. */
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

/** Get gutter width in pixels. */
function getGutterWidth(editor: HTMLElement): number {
    const gutters = editor.querySelector('.cm-gutters') as HTMLElement | null;
    return gutters ? gutters.offsetWidth : 0;
}

/** Get scrollbar width in pixels. */
function getScrollbarWidth(editor: HTMLElement): number {
    const scroller = editor.querySelector('.cm-scroller') as HTMLElement | null;
    return scroller ? scroller.offsetWidth - scroller.clientWidth : 0;
}

/** Find the nearest ancestor with .cm-line class. */
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
 * Get highlighted HTML for a declaration line.
 * Clones from the editor's DOM if available; otherwise falls back to plain text.
 */
export function getHeaderLineHtml(view: EditorView, header: SectionHeader): string {
    const line = view.state.doc.line(header.line);
    const plain = line.text.trim();
    const indent = line.text.match(/^(\s*)/)?.[1] ?? '';

    const lineStart = line.from;
    const dom = view.domAtPos(lineStart);
    const lineEl = findLineElement(dom?.node ?? null);
    if (lineEl) {
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
 * Sticky scroll ViewPlugin.
 *
 * Headers are provided via `updateHeadersEffect`.
 * The current section header sticks to the top until scrolling into the next section.
 */
export function stickyScroll() {
    return ViewPlugin.fromClass(
        class {
            element: HTMLElement;
            currentHeader: SectionHeader | null = null;
            // Cache rendered content to detect in-place edits (e.g. renames).
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

                // Filter out headers outside the document to prevent RangeError in doc.line().
                const lineCount = view.state.doc.lines;
                const validHeaders = headers.filter((h) => h.line >= 1 && h.line <= lineCount);
                if (validHeaders.length === 0) {
                    this.hide();
                    return;
                }

                const header = findStickyHeader(view, validHeaders);

                if (header) {
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
