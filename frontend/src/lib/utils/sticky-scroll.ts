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
 * Compute the pixel line height from CSS computed styles.
 */
function getLineHeightPx(scroller: HTMLElement): number {
    const style = getComputedStyle(scroller);
    const rawLineHeight = style.lineHeight;
    const rawFontSize = style.fontSize;

    if (rawLineHeight.endsWith('px')) {
        const px = parseFloat(rawLineHeight);
        if (!isNaN(px) && px > 0) return px;
    }

    const multiplier = parseFloat(rawLineHeight);
    if (!isNaN(multiplier) && multiplier > 0) {
        const fontSize = parseFloat(rawFontSize);
        if (!isNaN(fontSize) && fontSize > 0) return multiplier * fontSize;
    }

    const fontSize = parseFloat(rawFontSize);
    return !isNaN(fontSize) && fontSize > 0 ? fontSize * 1.5 : 21;
}

/**
 * Find the section header that should be sticky at the current scroll position.
 */
function findStickyHeader(
    view: EditorView,
    headers: SectionHeader[],
): SectionHeader | undefined {
    const scroller = view.dom.querySelector('.cm-scroller') as HTMLElement | null;
    if (!scroller || headers.length === 0) return undefined;

    const scrollTop = scroller.scrollTop;
    const lineHeight = getLineHeightPx(scroller);
    const topLine = Math.floor(scrollTop / lineHeight) + 1;

    let result: SectionHeader | undefined;
    for (const header of headers) {
        if (header.line <= topLine && topLine < header.endLine) {
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
 * Get the highlighted HTML for a declaration line by cloning from the
 * editor's DOM. Falls back to plain text if the line is not in viewport.
 */
function getHeaderLineHtml(view: EditorView, header: SectionHeader): string {
    const line = view.state.doc.line(header.line);
    const plain = line.text.trim();

    // Try to find the line's DOM in the visible viewport
    const lineStart = line.from;
    const dom = view.domAtPos(lineStart);
    if (dom && dom.node) {
        let el: Node | null = dom.node;
        let depth = 0;
        while (el && depth < 30) {
            if (el.nodeType === 1) {
                const cls = (el as Element).className;
                if (typeof cls === 'string' && cls.includes('cm-line')) {
                    // Clone the highlighted line content (has inline styles from defaultHighlightStyle)
                    const clone = (el as Element).cloneNode(true) as Element;
                    clone.removeAttribute('cm-line');
                    clone.removeAttribute('class');
                    clone.removeAttribute('style');
                    clone.removeAttribute('spellcheck');
                    const inner = clone.innerHTML;
                    if (inner && inner.length > plain.length) {
                        return inner;
                    }
                    break;
                }
            }
            el = el.parentNode;
            depth++;
        }
    }

    return escapeHtml(plain);
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

                const header = findStickyHeader(view, headers);

                if (header && (!this.currentHeader || header.line !== this.currentHeader.line)) {
                    this.currentHeader = header;
                    const html = getHeaderLineHtml(view, header);
                    this.element.innerHTML = html;
                    const gutterW = getGutterWidth(view.dom);
                    const scrollbarW = getScrollbarWidth(view.dom);
                    this.element.style.left = gutterW + 'px';
                    this.element.style.width = `calc(100% - ${gutterW}px - ${scrollbarW}px)`;
                    this.element.style.display = 'block';
                } else if (!header && this.currentHeader) {
                    this.currentHeader = null;
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
