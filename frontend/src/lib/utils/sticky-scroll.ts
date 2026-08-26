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
 * Detach a few lines early so the bar doesn't cover the next header.
 */
const HEADER_DETACH_MARGIN = 3;

/**
 * Find the header to pin at the current scroll position.
 * `headers` must be sorted by `line`. Sections don't overlap, so the pinned
 * header is the last one starting at/above the top line (binary search), then
 * dropped if it ends too soon.
 */
export function findStickyHeader(
    view: EditorView,
    headers: SectionHeader[],
): SectionHeader | undefined {
    if (headers.length === 0) return undefined;

    const topLine = view.state.doc.lineAt(view.viewport.from).number;

    // Rightmost header whose start is at or above the viewport top.
    let lo = 0;
    let hi = headers.length - 1;
    let candidate: SectionHeader | undefined;
    while (lo <= hi) {
        const mid = (lo + hi) >> 1;
        if (headers[mid].line <= topLine) {
            candidate = headers[mid];
            lo = mid + 1;
        } else {
            hi = mid - 1;
        }
    }

    if (!candidate) return undefined;

    // Stop pinning a few lines early so the next header isn't covered.
    if (topLine < candidate.endLine - HEADER_DETACH_MARGIN) return candidate;

    return undefined;
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
 * Sticky scroll plugin: pins the current section header to the top.
 * Headers come in via `updateHeadersEffect`.
 *
 * Performance: scroll is batched per frame, `update()` ignores cursor-only
 * moves, header lookup is a binary search, and widths are cached.
 */
export function stickyScroll() {
    return ViewPlugin.fromClass(
        class StickyScroll {
            element: HTMLElement;
            currentHeader: SectionHeader | null = null;
            // Cache the rendered line's text to detect in-place edits (e.g. renames).
            renderedLineText = '';

            // Cached geometry (re-measured via ResizeObserver, not per render).
            private gutterWidth = 0;
            private scrollbarWidth = 0;

            private view: EditorView;
            private scrollHandler: (() => void) | null = null;
            private frame: number | null = null;
            private resizeObserver: ResizeObserver | null = null;

            constructor(view: EditorView) {
                this.view = view;
                this.element = document.createElement('div');
                this.element.className = 'cm-sticky-scroll';
                this.element.setAttribute('aria-hidden', 'true');

                // view.dom is the editor root (positioned relatively by
                // CodeMirror), so an absolute child at top:0 stays pinned.
                if (view.dom.parentElement) {
                    this.insert(view);
                }
            }

            /** Attach the bar + listeners once the editor is in the DOM. */
            private insert(view: EditorView) {
                if (this.element.parentElement) return;
                view.dom.appendChild(this.element);

                const scroller = view.scrollDOM;
                if (scroller) {
                    this.scrollHandler = () => this.scheduleScrollUpdate();
                    scroller.addEventListener('scroll', this.scrollHandler, { passive: true });
                }

                this.measureWidths(view);
                if (typeof ResizeObserver !== 'undefined') {
                    this.resizeObserver = new ResizeObserver(() => this.onResize(view));
                    this.resizeObserver.observe(view.dom);
                }

                this.updateSticky(view);
            }

            /** Coalesce high-frequency scroll events into one update per frame. */
            private scheduleScrollUpdate() {
                if (this.frame !== null) return;
                this.frame = requestAnimationFrame(() => {
                    this.frame = null;
                    this.updateSticky(this.view);
                });
            }

            private onResize(view: EditorView) {
                this.measureWidths(view);
                // Keep the overlay aligned even if only geometry changed.
                if (this.currentHeader && this.element.style.display !== 'none') {
                    this.position();
                }
            }

            private measureWidths(view: EditorView) {
                this.gutterWidth = getGutterWidth(view.dom);
                this.scrollbarWidth = getScrollbarWidth(view.dom);
            }

            /** Apply cached gutter/scrollbar offsets to the overlay. */
            private position() {
                this.element.style.left = this.gutterWidth + 'px';
                this.element.style.width = `calc(100% - ${this.gutterWidth}px - ${this.scrollbarWidth}px)`;
            }

            update(update: ViewUpdate) {
                // If the view was created detached from the DOM, insert the bar
                // on the first update. insert() recomputes, so we're done here.
                if (!this.element.parentElement && update.view.dom.parentElement) {
                    this.insert(update.view);
                    return;
                }

                // Only recompute on edits, scrolls, or new headers — skip cursor moves.
                const headersChanged = update.transactions.some((tr) =>
                    tr.effects.some((e) => e.is(updateHeadersEffect)),
                );
                if (update.docChanged || update.viewportChanged || headersChanged) {
                    this.updateSticky(update.view);
                }
            }

            /** Whether the overlay content needs re-rendering for this header. */
            private shouldRender(header: SectionHeader, lineText: string): boolean {
                // Compare to the last rendered header to skip unchanged scrolls.
                return (
                    !this.currentHeader ||
                    this.currentHeader.line !== header.line ||
                    this.currentHeader.endLine !== header.endLine ||
                    this.renderedLineText !== lineText
                );
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
                    if (this.shouldRender(header, lineText)) {
                        this.currentHeader = header;
                        this.renderedLineText = lineText;
                        this.element.innerHTML = getHeaderLineHtml(view, header);
                        this.measureWidths(view);
                        this.position();
                        this.element.style.display = 'block';
                    }
                } else if (this.currentHeader) {
                    this.currentHeader = null;
                    this.renderedLineText = '';
                    this.hide();
                }
            }

            hide() {
                this.element.style.display = 'none';
            }

            destroy() {
                if (this.frame !== null) {
                    cancelAnimationFrame(this.frame);
                    this.frame = null;
                }
                const scroller = this.view.scrollDOM;
                if (scroller && this.scrollHandler) {
                    scroller.removeEventListener('scroll', this.scrollHandler);
                }
                this.scrollHandler = null;
                this.resizeObserver?.disconnect();
                this.resizeObserver = null;
                this.element.remove();
            }
        },
        {},
    );
}
