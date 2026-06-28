import { EditorView, ViewPlugin, ViewUpdate, gutter, GutterMarker } from '@codemirror/view';
import { RangeSet, Range, type Extension } from '@codemirror/state';
import { computeDiffMarkers, type DiffMarker } from '$lib/utils/diff-gutter';

// --- Gutter marker classes ---

class AddedMarker extends GutterMarker {
    toDOM() {
        const el = document.createElement('div');
        el.className = 'diff-gutter-added';
        return el;
    }
    elementClass = 'diff-marker-added';
}

class ModifiedMarker extends GutterMarker {
    constructor(readonly removedText: string) { super(); }
    toDOM() {
        const el = document.createElement('div');
        el.className = 'diff-gutter-modified';
        el.title = this.removedText;
        return el;
    }
    elementClass = 'diff-marker-modified';
    eq(other: GutterMarker): boolean {
        return other instanceof ModifiedMarker && other.removedText === this.removedText;
    }
}

class RemovedMarker extends GutterMarker {
    constructor(readonly removedText: string, readonly expanded: boolean) { super(); }
    toDOM() {
        const el = document.createElement('div');
        el.className = 'diff-gutter-removed';

        const toggle = document.createElement('span');
        toggle.className = 'diff-removed-toggle';
        toggle.textContent = this.expanded ? '▾' : '▸';
        toggle.title = this.removedText;
        el.appendChild(toggle);

        if (this.expanded) {
            const preview = document.createElement('span');
            preview.className = 'diff-removed-preview';
            preview.textContent = this.removedText;
            el.appendChild(preview);
        }

        return el;
    }
    elementClass = 'diff-marker-removed';
    eq(other: GutterMarker): boolean {
        return other instanceof RemovedMarker
            && other.removedText === this.removedText
            && other.expanded === this.expanded;
    }
}

// --- Shared state (per editor instance) ---

interface DiffGutterState {
    markers: DiffMarker[];
    expandedRemoved: Set<number>;
}

// --- Extension factory ---

/** Create a CodeMirror extension that adds VS Code-style diff indicators in a custom gutter.
 *
 * @param originalContent - The git HEAD (baseline) content to diff against.
 *   Pass empty string or undefined for untracked files (all lines shown as added).
 */
export function diffGutterExtension(originalContent: string | undefined): Extension[] {
    const state: DiffGutterState = {
        markers: [],
        expandedRemoved: new Set(),
    };

    // Compute initial markers
    if (originalContent) {
        state.markers = computeDiffMarkers(originalContent, originalContent);
    }

    // ViewPlugin: compute diff markers and handle click events
    const plugin = ViewPlugin.fromClass(
        class {
            constructor(view: EditorView) {
                const content = view.state.doc.toString();
                state.markers = originalContent
                    ? computeDiffMarkers(originalContent, content)
                    : buildAllAddedMarkers(content);
            }

            update(update: ViewUpdate) {
                if (update.docChanged) {
                    const content = update.view.state.doc.toString();
                    state.markers = originalContent
                        ? computeDiffMarkers(originalContent, content)
                        : buildAllAddedMarkers(content);
                    state.expandedRemoved = new Set();
                }
            }
        },
        {
            eventHandlers: {
                click: (event, view) => {
                    const target = event.target as HTMLElement;
                    if (!target.classList.contains('diff-removed-toggle')) return;
                    event.preventDefault();
                    event.stopPropagation();

                    // Find line number from the gutter element's data attribute
                    const gutterElement = target.closest('.cm-gutterElement') as HTMLElement | null;
                    if (!gutterElement) return;
                    const lineNum = gutterElement.dataset.cmLine;
                    if (!lineNum) return;
                    const num = parseInt(lineNum, 10);
                    if (isNaN(num)) return;

                    if (state.expandedRemoved.has(num)) {
                        state.expandedRemoved.delete(num);
                    } else {
                        state.expandedRemoved.add(num);
                    }
                    // Trigger re-render
                    view.dispatch({});
                },
            },
        },
    );

    // Custom gutter that reads from shared state
    const diffGutter = gutter({
        class: 'cm-diff-gutter',
        markers: (view) => {
            return [buildGutterMarkers(view, state)];
        },
        initialSpacer: () => new (class extends GutterMarker {
            toDOM() {
                const el = document.createElement('div');
                el.className = 'diff-gutter-spacer';
                return el;
            }
        })(),
    });

    return [diffGutter, plugin];
}

/** Build a RangeSet of gutter markers from the diff state. */
function buildGutterMarkers(view: EditorView, state: DiffGutterState): RangeSet<GutterMarker> {
    const doc = view.state.doc;
    const array: Array<Range<GutterMarker>> = [];

    for (let lineNum = 1; lineNum <= doc.lines; lineNum++) {
        const marker = state.markers[lineNum];
        if (!marker) continue;

        let gutterMarker: GutterMarker | null = null;
        switch (marker.type) {
            case 'added':
                gutterMarker = new AddedMarker();
                break;
            case 'modified':
                gutterMarker = new ModifiedMarker(marker.removedText);
                break;
            case 'removed':
                gutterMarker = new RemovedMarker(
                    marker.text,
                    state.expandedRemoved.has(lineNum),
                );
                break;
            case 'context':
                continue;
        }

        if (gutterMarker) {
            const line = doc.line(lineNum);
            array.push(gutterMarker.range(line.from));
        }
    }

    return RangeSet.of(array, false);
}

function buildAllAddedMarkers(content: string): DiffMarker[] {
    const lines = content.split('\n');
    const markers: DiffMarker[] = [];
    for (let i = 1; i <= lines.length; i++) {
        markers[i] = { type: 'added' };
    }
    return markers;
}
