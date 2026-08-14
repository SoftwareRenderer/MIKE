import { ViewPlugin, EditorView, hasHoverTooltips, closeHoverTooltips } from '@codemirror/view';

// Close any open hover tooltips when the editor scrolls.
export function closeHoverOnScroll() {
    return ViewPlugin.fromClass(
        class {
            private readonly scroller: HTMLElement;
            private readonly onScroll: () => void;

            constructor(view: EditorView) {
                this.scroller = view.scrollDOM;
                this.onScroll = () => {
                    if (hasHoverTooltips(view.state)) {
                        view.dispatch({ effects: closeHoverTooltips });
                    }
                };
                this.scroller.addEventListener('scroll', this.onScroll, { passive: true });
            }

            destroy() {
                this.scroller.removeEventListener('scroll', this.onScroll);
            }
        },
    );
}
