import {
    EditorView,
    gutter,
    GutterMarker,
} from '@codemirror/view';
import { mergeMarkers } from './diff-gutter';

interface IndicatorDef {
    color: string;
    shape: 'stripe' | 'triangle';
    pattern: [number, number];
}

/** Gutter marker that renders colored indicator(s) + line number. */
class GitLineMarker extends GutterMarker {
    readonly lineNum: number;
    readonly indicators: IndicatorDef[];

    constructor(lineNum: number, indicators: IndicatorDef[]) {
        super();
        this.lineNum = lineNum;
        this.indicators = indicators;
    }

    toDOM(): HTMLElement {
        const el = document.createElement('div');
        el.style.cssText = 'display:flex;align-items:center;gap:4px;height:100%';

        // Overlay of indicators (positioned on top of each other)
        const overlay = document.createElement('span');
        overlay.style.cssText =
            'position:relative;display:inline-block;width:10px;height:100%;flex-shrink:0';

        for (const ind of this.indicators) {
            const indicator = document.createElement('span');
            if (ind.shape === 'triangle') {
                // Triangle anchored to bottom-left of overlay
                indicator.style.cssText =
                    'position:absolute;bottom:0;left:2px;' +
                    'width:6px;height:6px;' +
                    'background:' + ind.color + ';' +
                    'clip-path:polygon(0 0, 0 100%, 100% 100%)';
            } else {
                // Stripe centered in overlay
                const [colorPx, gapPx] = ind.pattern;
                const total = colorPx + gapPx;
                indicator.style.cssText =
                    'position:absolute;top:0;left:2px;' +
                    'display:block;width:4px;height:100%;border-radius:1px;' +
                    'background:repeating-linear-gradient(-45deg,' + ind.color + ' 0px,' +
                    ind.color + ' ' + colorPx + 'px,transparent ' + colorPx + 'px,transparent ' +
                    total + 'px)';
            }
            overlay.appendChild(indicator);
        }

        const num = document.createElement('span');
        num.textContent = String(this.lineNum);
        el.appendChild(overlay);
        el.appendChild(num);
        return el;
    }

    compareBeforeUpdate(other: GutterMarker): boolean {
        if (!(other instanceof GitLineMarker)) return false;
        if (other.lineNum !== this.lineNum || other.indicators.length !== this.indicators.length)
            return false;
        for (let i = 0; i < this.indicators.length; i++) {
            const a = this.indicators[i];
            const b = other.indicators[i];
            if (a.color !== b.color || a.shape !== b.shape || a.pattern[0] !== b.pattern[0] || a.pattern[1] !== b.pattern[1])
                return false;
        }
        return true;
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
        spacer.style.cssText = 'width:10px;flex-shrink:0';
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
 *
 * @param initialMarkers - Pre-computed markers from server (diff vs HEAD), keyed by baseline line number
 * @param baselineContent - File content as read from disk (before user edits)
 * @param current - Last computed document content (for change detection)
 * @param map - Merged markers keyed by current document line number
 */
export interface GitGutterState {
    initialMarkers: Map<number, string[]> | null;
    baselineContent: string;
    current: string;
    map: Map<number, ('added' | 'modified' | 'removed')[]>;
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
 *   state.initialMarkers = serverMarkers; // from /api/fs/read response
 *   state.baselineContent = fileContent;  // content from disk
 *   // After save (re-fetch file):
 *   state.initialMarkers = newMarkers;
 *   state.baselineContent = newContent;
 */
export function gitGutter() {
    const state: GitGutterState = { initialMarkers: null, baselineContent: '', current: '', map: new Map() };

    const gutterDef = gutter({
        class: 'cm-gitGutter',
        lineMarker: (view, line) => {
            const ln = view.state.doc.lineAt(line.from).number;
            const current = view.state.doc.toString();

            // Recompute when content changes
            if (current !== state.current) {
                state.current = current;
                const merged = mergeMarkers(state.initialMarkers, state.baselineContent, current);
                // Convert to typed map
                const map = new Map<number, ('added' | 'modified' | 'removed')[]>();
                for (const [ln, types] of merged) {
                    map.set(ln, types as ('added' | 'modified' | 'removed')[]);
                }
                state.map = map;
            }

            const types = state.map.get(ln);
            if (types && types.length > 0) {
                const indicators: IndicatorDef[] = [];
                const colorMap: Record<string, string> = {
                    added: 'rgba(63,185,80,0.7)',
                    modified: 'rgba(88,166,255,0.7)',
                    removed: 'rgba(248,81,73,0.7)',
                };
                for (const t of types) {
                    if (t === 'removed') {
                        indicators.push({ color: colorMap[t], shape: 'triangle', pattern: [0, 0] as [number, number] });
                    } else {
                        const pattern: [number, number] = t === 'added' ? [2, 0] : [2, 1];
                        indicators.push({ color: colorMap[t], shape: 'stripe', pattern });
                    }
                }
                return new GitLineMarker(ln, indicators);
            }
            return new PlainLineMarker(ln);
        },
    });

    const theme = EditorView.baseTheme({
        '.cm-gutter.cm-gitGutter': {
            minWidth: '36px',
            fontSize: 'inherit',
            color: 'inherit',
        },
    });

    return { extension: [gutterDef, theme], state };
}
