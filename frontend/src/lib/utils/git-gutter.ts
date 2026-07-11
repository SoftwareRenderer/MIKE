import {
    EditorView,
    gutter,
    GutterMarker,
} from '@codemirror/view';
import { computeDiffMarkers } from './diff-gutter';

/** Gutter marker that renders a colored stripe + line number. */
class GitLineMarker extends GutterMarker {
    readonly lineNum: number;
    readonly color: string;

    constructor(lineNum: number, color: string) {
        super();
        this.lineNum = lineNum;
        this.color = color;
    }

    toDOM(): HTMLElement {
        const el = document.createElement('div');
        el.style.cssText = 'display:flex;align-items:center;gap:4px;height:100%';
        const stripe = document.createElement('span');
        stripe.style.cssText =
            'display:block;width:4px;height:100%;border-radius:1px;flex-shrink:0;' +
            'background:repeating-linear-gradient(-45deg,' + this.color + ' 0px,' +
            this.color + ' 2px,transparent 2px,transparent 4px)';
        const num = document.createElement('span');
        num.textContent = String(this.lineNum);
        el.appendChild(stripe);
        el.appendChild(num);
        return el;
    }

    compareBeforeUpdate(other: GutterMarker): boolean {
        return (
            other instanceof GitLineMarker &&
            other.lineNum === this.lineNum &&
            other.color === this.color
        );
    }
}

/** Plain line number marker (no colored stripe). */
class PlainLineMarker extends GutterMarker {
    readonly lineNum: number;

    constructor(lineNum: number) {
        super();
        this.lineNum = lineNum;
    }

    toDOM(): HTMLElement {
        const el = document.createElement('div');
        el.style.cssText = 'display:flex;align-items:center;gap:4px;height:100%';
        const spacer = document.createElement('span');
        spacer.style.cssText = 'width:4px;flex-shrink:0';
        const num = document.createElement('span');
        num.textContent = String(this.lineNum);
        el.appendChild(spacer);
        el.appendChild(num);
        return el;
    }

    compareBeforeUpdate(other: GutterMarker): boolean {
        return other instanceof PlainLineMarker && other.lineNum === this.lineNum;
    }
}

/** Per-editor mutable state — updated directly by editors after save.
 * Avoids CodeMirror state transactions entirely (no stale view issues).
 */
export interface GitGutterState {
    original: string;
    current: string;
    map: Map<number, 'added' | 'modified'>;
}

/** CodeMirror extension for VS Code-style git gutter indicators.
 *
 * Shows colored stripes to the left of line numbers:
 * - Green for added lines
 * - Blue for modified lines
 *
 * Usage:
 *   const { extension, state } = gitGutter();
 *   // ... create editor with extension ...
 *   // After save:
 *   state.original = newContent;
 */
export function gitGutter() {
    const state: GitGutterState = { original: '', current: '', map: new Map() };

    const gutterDef = gutter({
        class: 'cm-gitGutter',
        lineMarker: (view, line) => {
            const ln = view.state.doc.lineAt(line.from).number;
            const original = state.original;
            const current = view.state.doc.toString();

            // Recompute when content changes
            if (original !== state.original || current !== state.current) {
                state.original = original;
                state.current = current;
                const map = new Map<number, 'added' | 'modified'>();
                if (original && original !== current) {
                    const markers = computeDiffMarkers(original, current);
                    for (let i = 1; i < markers.length; i++) {
                        const m = markers[i];
                        if (m && m.type !== 'context' && m.type !== 'removed') {
                            map.set(i, m.type as 'added' | 'modified');
                        }
                    }
                }
                state.map = map;
            }

            const type = state.map.get(ln);
            if (type) {
                const color =
                    type === 'added'
                        ? 'rgba(63,185,80,0.7)'
                        : 'rgba(88,166,255,0.7)';
                return new GitLineMarker(ln, color);
            }
            return new PlainLineMarker(ln);
        },
    });

    const theme = EditorView.baseTheme({
        '.cm-gutter.cm-gitGutter': {
            minWidth: '48px',
            fontSize: 'inherit',
            color: 'inherit',
        },
    });

    return { extension: [gutterDef, theme], state };
}
