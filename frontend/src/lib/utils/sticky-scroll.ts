import { ViewPlugin, ViewUpdate, EditorView } from '@codemirror/view';
import { StateEffect, StateField } from '@codemirror/state';

/** A raw declaration scope range (LSP symbol) before nesting depth is assigned. */
export interface ScopeRange {
    line: number;
    endLine: number;
}

/** Section header for sticky scroll. */
export interface SectionHeader {
    line: number,
    endLine: number; // last line in section
    depth: number;   // nesting level (0 = top-level)
}

/**
 * Detach the innermost bar a few lines early so it doesn't cover the next header.
 */
const HEADER_DETACH_MARGIN = 3;

/** Maximum number of sticky bars stacked at once (outermost → innermost). */
export const MAX_STICKY_DEPTH = 4;

/**
 * Turn declaration scopes (LSP document symbols) into a sorted,
 * depth-annotated sticky-header list.
 *
 * Every scope keeps its FULL body range (endLine = its own closing line, not
 * truncated to the next sibling). Overlapping ranges are resolved by sorting
 * start ascending and end descending, so an outer scope always precedes the
 * inner scopes it contains. `depth` records the containment nesting level.
 */
export function buildSectionHeaders(declarations: ScopeRange[]): SectionHeader[] {
    const combined: ScopeRange[] = [...declarations];
    combined.sort((a, b) => a.line - b.line || b.endLine - a.endLine);

    const headers: SectionHeader[] = [];
    const stack: SectionHeader[] = []; // ancestor path, innermost last

    for (const scope of combined) {
        // Drop ancestors that no longer contain this scope.
        while (stack.length && stack[stack.length - 1].endLine < scope.endLine) {
            stack.pop();
        }
        const header: SectionHeader = {
            line: scope.line,
            endLine: scope.endLine,
            depth: stack.length,
        };
        stack.push(header);
        headers.push(header);
    }

    return headers;
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
 * The line number at the top of the visible viewport
 */
export function getViewportTopLine(view: EditorView): number {
    try {
        const scroller = view.scrollDOM;
        if (scroller) {
            const rect = scroller.getBoundingClientRect();
            if (rect.width > 0 && rect.height > 0) {
                // Probe the gutter first (x = +8), then the content's left
                // edge; a few pixel rows down to dodge exact boundaries.
                for (const dx of [8, 0]) {
                    for (const dy of [2, 8, 16]) {
                        const pos = view.posAtCoords({ x: rect.left + dx, y: rect.top + dy });
                        if (pos != null) {
                            return view.state.doc.lineAt(pos).number;
                        }
                    }
                }
            }
        }
    } catch {
        /* fall through to the estimate */
    }
    return view.state.doc.lineAt(view.viewport.from).number;
}

/**
 * Compute the sticky-bar stack at the current scroll position.
 *
 * Returns the ordered chain of scopes enclosing the top-of-viewport line,
 * outermost → innermost (e.g. `[class Foo, function bar]`).
 *
 * The innermost bar detaches a few lines before its scope ends so it never
 * covers the next sibling header; enclosing scopes stay pinned while active.
 * The stack is capped at MAX_STICKY_DEPTH bars (outermost dropped first).
 *
 * `headers` must be sorted by `line` (see buildSectionHeaders).
 */
export function findStickyStack(
    view: EditorView,
    headers: SectionHeader[],
): SectionHeader[] {
    if (headers.length === 0) return [];

    const topLine = getViewportTopLine(view);

    // Innermost scope containing the top line = the one with the greatest start
    // line that still spans the top line.
    let innermost: SectionHeader | null = null;
    for (const h of headers) {
        if (h.line <= topLine && topLine <= h.endLine) {
            innermost = h;
        }
    }
    if (!innermost) return [];

    // Collect enclosing ancestors (contain the innermost's full range).
    const ancestors: SectionHeader[] = [];
    for (const h of headers) {
        if (h.line < innermost.line && h.endLine >= innermost.endLine) {
            ancestors.push(h);
        }
    }

    // The innermost bar detaches a few lines early so it doesn't cover the
    // next header; enclosing scopes stay pinned while they remain active.
    const detachInnermost = topLine >= innermost.endLine - HEADER_DETACH_MARGIN;
    let stack = detachInnermost ? ancestors : [...ancestors, innermost];

    // Keep the innermost MAX_STICKY_DEPTH bars (drop outermost if exceeded).
    if (stack.length > MAX_STICKY_DEPTH) {
        stack = stack.slice(stack.length - MAX_STICKY_DEPTH);
    }
    return stack;
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
 * Sticky scroll plugin: pins the stack of enclosing declaration headers
 * (e.g. class → function) to the top of the editor. Headers come in via
 * `updateHeadersEffect`; one `.cm-sticky-scroll-row` is rendered per scope,
 * outermost first.
 *
 * Performance: scroll is batched per frame, `update()` ignores cursor-only
 * moves, re-renders are skipped while the pinned stack is unchanged, and
 * widths are cached (re-measured via ResizeObserver).
 */
export function stickyScroll() {
    return ViewPlugin.fromClass(
        class StickyScroll {
            element: HTMLElement;
            currentStack: SectionHeader[] = [];
            // Cache a signature of the rendered stack (line + text per row) to
            // detect in-place edits (e.g. renames) and skip unchanged scrolls.
            renderedSignature = '';

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
                if (this.currentStack.length > 0 && this.element.style.display !== 'none') {
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

                const stack = findStickyStack(view, validHeaders);

                if (stack.length > 0) {
                    // Signature = each row's line + text; re-render only when the
                    // pinned set or any header line's content actually changed.
                    const signature = stack
                        .map((h) => `${h.line}\u0000${view.state.doc.line(h.line).text}`)
                        .join('\u0001');
                    if (signature !== this.renderedSignature) {
                        this.renderedSignature = signature;
                        this.currentStack = stack;
                        this.renderStack(view, stack);
                    }
                } else if (this.currentStack.length > 0) {
                    this.currentStack = [];
                    this.renderedSignature = '';
                    this.hide();
                }
            }

            /** Render one bar per enclosing scope (outermost → innermost). */
            private renderStack(view: EditorView, stack: SectionHeader[]) {
                this.element.innerHTML = '';
                for (const header of stack) {
                    const row = document.createElement('div');
                    row.className = 'cm-sticky-scroll-row';
                    row.innerHTML = getHeaderLineHtml(view, header);
                    this.element.appendChild(row);
                }
                this.measureWidths(view);
                this.position();
                this.element.style.display = 'block';
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
