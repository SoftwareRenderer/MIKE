import { EditorView, ViewPlugin, hasHoverTooltips, closeHoverTooltips } from '@codemirror/view';
import { inBox } from './hit-box';

/**
 * Close any open hover tooltip when the editor scrolls — except when the
 * pointer is inside the tooltip itself.
 *
 * What is left in the hover-plugin family here is the diagnostics hover
 * (`serverDiagnostics` renders through `@codemirror/lint`, whose hover is a
 * `hoverTooltip`); the go-to-definition card is not a hover tooltip and
 * dismisses itself — see `lsp-hover-actions.ts`. For either kind the rule is
 * the same: a trackpad nudge that begins while the pointer is resting on the
 * box is the user reading it, so the box stays. A scroll that starts anywhere
 * else means the user moved on, and the tooltip would be left pointing at text
 * it is not about.
 */
export function closeHoverOnScroll() {
    return ViewPlugin.fromClass(
        class {
            private readonly scroller: HTMLElement;
            private readonly onScroll: () => void;
            private readonly onMove: (event: MouseEvent) => void;
            private pointer: { x: number; y: number } | null = null;

            constructor(private readonly view: EditorView) {
                this.scroller = view.scrollDOM;
                this.onScroll = () => {
                    if (!hasHoverTooltips(view.state)) return;
                    if (this.pointerIsInTooltip()) return;
                    view.dispatch({ effects: closeHoverTooltips });
                };
                this.onMove = event => {
                    this.pointer = { x: event.clientX, y: event.clientY };
                };
                this.scroller.addEventListener('scroll', this.onScroll, { passive: true });
                view.dom.addEventListener('mousemove', this.onMove, { passive: true });
            }

            private pointerIsInTooltip(): boolean {
                if (!this.pointer) return false;
                const tooltip = this.view.dom.querySelector('.cm-tooltip-hover');
                if (!tooltip) return false;
                return inBox(tooltip.getBoundingClientRect(), this.pointer.x, this.pointer.y);
            }

            destroy() {
                this.scroller.removeEventListener('scroll', this.onScroll);
                this.view.dom.removeEventListener('mousemove', this.onMove);
            }
        },
    );
}
