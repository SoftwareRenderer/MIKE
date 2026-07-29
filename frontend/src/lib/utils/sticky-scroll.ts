import { ViewPlugin, ViewUpdate, EditorView } from '@codemirror/view';
import { getLanguageTreeFromView } from '$lib/highlighter';

/** Section header for sticky scroll. */
interface SectionHeader {
    line: number;
}

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
 * Extract section headers from the editor using the existing symbol extractor.
 * Filters to functions and classes only, sorted by line number.
 */
function extractSectionHeaders(view: EditorView, filePath: string): SectionHeader[] {
    try {
        const symbols = getLanguageTreeFromView(view, filePath);
        return symbols
            .filter((s) => s.kind === 'function' || s.kind === 'class')
            .sort((a, b) => a.line - b.line);
    } catch {
        return [];
    }
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
        if (header.line <= topLine) {
            result = header;
        } else {
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
 * Create a sticky scroll ViewPlugin configured with the given file path.
 *
 * When scrolling through code, the current section header (function/class
 * declaration) sticks to the top of the editor until scrolling into the
 * next section.
 */
export function stickyScroll(filePath: string) {
    return ViewPlugin.fromClass(
        class {
            element: HTMLElement;
            headers: SectionHeader[] = [];
            currentHeader: SectionHeader | null = null;
            viewRef: EditorView | null = null;
            extractTimer: ReturnType<typeof setTimeout> | null = null;
            needsExtract = false;

            constructor(view: EditorView) {
                this.element = document.createElement('div');
                this.element.className = 'cm-sticky-scroll';
                this.element.setAttribute('aria-hidden', 'true');

                const insert = () => {
                    if (view.dom.parentElement && !this.element.parentElement) {
                        // Insert as child of .cm-editor (not .cm-scroller) to avoid
                        // stacking context issues with .cm-sizer
                        view.dom.appendChild(this.element);
                        this.setupScrollListener(view);
                        this.headers = extractSectionHeaders(view, filePath);
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
                if (!this.viewRef) this.viewRef = update.view;
                const view = update.view;

                if (update.docChanged) {
                    this.needsExtract = true;
                    if (!this.extractTimer) {
                        this.extractTimer = setTimeout(() => {
                            this.extractTimer = null;
                            if (this.needsExtract && this.viewRef) {
                                this.headers = extractSectionHeaders(this.viewRef, filePath);
                                this.needsExtract = false;
                                this.updateSticky(this.viewRef!);
                            }
                        }, 300);
                    }
                }

                if (update.viewportChanged) {
                    this.updateSticky(view);
                }
            }

            updateSticky(view: EditorView) {
                if (this.headers.length === 0) {
                    this.hide();
                    return;
                }

                const header = findStickyHeader(view, this.headers);

                if (header && header !== this.currentHeader) {
                    this.currentHeader = header;
                    const html = getHeaderLineHtml(view, header);
                    this.element.innerHTML = html;
                    // Offset past gutters
                    const gutterW = getGutterWidth(view.dom);
                    this.element.style.left = gutterW + 'px';
                    this.element.style.width = `calc(100% - ${gutterW}px)`;
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
                if (this.extractTimer) {
                    clearTimeout(this.extractTimer);
                    this.extractTimer = null;
                }
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
