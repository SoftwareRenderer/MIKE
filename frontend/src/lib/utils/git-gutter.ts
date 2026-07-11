import {
    EditorView,
    gutter,
    GutterMarker,
} from '@codemirror/view';
import { computeDiffMarkers } from './diff-gutter';

/** Gutter marker that renders a colored indicator + line number. */
class GitLineMarker extends GutterMarker {
    readonly lineNum: number;
    readonly color: string;
    readonly shape: 'stripe' | 'triangle';
    readonly pattern: [number, number]; // [color px, gap px]

    constructor(
        lineNum: number,
        color: string,
        shape: 'stripe' | 'triangle' = 'stripe',
        pattern: [number, number] = [2, 2],
    ) {
        super();
        this.lineNum = lineNum;
        this.color = color;
        this.shape = shape;
        this.pattern = pattern;
    }

    toDOM(): HTMLElement {
        const el = document.createElement('div');
        el.style.cssText =
            'display:flex;' +
            (this.shape === 'triangle' ? 'align-items:flex-end' : 'align-items:center') +
            ';gap:4px;height:100%';
        const indicator = document.createElement('span');

        if (this.shape === 'triangle') {
            // Right triangle: vertices [0,1], [0,0], [1,0] — fills bottom-left corner
            indicator.style.cssText =
                'width:8px;height:8px;flex-shrink:0;' +
                'background:' + this.color + ';' +
                'clip-path:polygon(0 0, 0 100%, 100% 100%)';
        } else {
            // Diagonal striped pattern
            const [colorPx, gapPx] = this.pattern;
            const total = colorPx + gapPx;
            indicator.style.cssText =
                'display:block;width:4px;height:100%;border-radius:1px;flex-shrink:0;' +
                'background:repeating-linear-gradient(-45deg,' + this.color + ' 0px,' +
                this.color + ' ' + colorPx + 'px,transparent ' + colorPx + 'px,transparent ' +
                total + 'px)';
        }

        const num = document.createElement('span');
        num.textContent = String(this.lineNum);
        el.appendChild(indicator);
        el.appendChild(num);
        return el;
    }

    compareBeforeUpdate(other: GutterMarker): boolean {
        return (
            other instanceof GitLineMarker &&
            other.lineNum === this.lineNum &&
            other.color === this.color &&
            other.shape === this.shape &&
            other.pattern[0] === this.pattern[0] &&
            other.pattern[1] === this.pattern[1]
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
    map: Map<number, 'added' | 'modified' | 'removed'>;
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
                const map = new Map<number, 'added' | 'modified' | 'removed'>();
                if (original && original !== current) {
                    const markers = computeDiffMarkers(original, current);
                    for (let i = 1; i < markers.length; i++) {
                        const m = markers[i];
                        if (m && m.type !== 'context') {
                            map.set(i, m.type as 'added' | 'modified' | 'removed');
                        }
                    }
                }
                state.map = map;
            }

            const type = state.map.get(ln);
            if (type === 'added') {
                return new GitLineMarker(ln, 'rgba(63,185,80,0.7)', 'stripe', [2, 0]);
            } else if (type === 'modified') {
                return new GitLineMarker(ln, 'rgba(88,166,255,0.7)', 'stripe', [2, 1]);
            } else if (type === 'removed') {
                return new GitLineMarker(ln, 'rgba(248,81,73,0.7)', 'triangle');
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
