/**
 * Pure helpers for the "select lines in the editor gutter → create task"
 * flow (gutter clicks in the Explorer file editors; multi-line selections
 * come from the editor's own text selection).
 *
 * No CodeMirror or DOM dependencies — unit tested in
 * `__tests__/line-selection.unit.ts`.
 */

import { sandboxMountFromPath } from './path';

/** Inclusive 1-based document line range. */
export interface LineRange {
    start: number;
    end: number;
}

/**
 * Resolve the inclusive line range for a gutter click.
 *
 * If the editor's text selection spans more than one line (`highlighted`,
 * the inclusive range of the touched lines) and the clicked line falls
 * inside it, the whole highlighted range is selected. Otherwise the clicked
 * line alone is selected.
 */
export function resolveGutterClickRange(
    clickedLine: number,
    highlighted: LineRange | null,
): LineRange {
    if (
        highlighted &&
        highlighted.end > highlighted.start &&
        clickedLine >= highlighted.start &&
        clickedLine <= highlighted.end
    ) {
        return { start: highlighted.start, end: highlighted.end };
    }
    return { start: clickedLine, end: clickedLine };
}

/**
 * Project-relative display path for the task description header.
 *
 * Accepts the editor's file path, which is either the host project path
 * (`<projectPath>/...`) or the sandbox mount path
 * (`/home/sandbox/<projectName>/...`). Anything unrecognized is returned
 * as-is so the description always carries a usable path.
 */
export function toProjectRelative(filePath: string, projectPath: string): string {
    if (!filePath) return '';
    const root = (projectPath || '').replace(/\/+$/, '');
    if (root && filePath.startsWith(root + '/')) return filePath.slice(root.length + 1);
    const mount = sandboxMountFromPath(root);
    if (mount && filePath.startsWith(mount + '/')) return filePath.slice(mount.length + 1);
    return filePath;
}

/**
 * Build the prefilled task description for a gutter line selection:
 *
 *     src/main.go:10-12
 *     10: func main() {
 *     11: \tfmt.Println("hi")
 *     12: }
 *
 * `startLine` is the 1-based first selected line; `lines` holds the line
 * texts in order (length ≥ 1). A single selected line gets a `path:N`
 * header without a range.
 */
function formatLineSelection(relativePath: string, startLine: number, lines: string[]): string {
    const path = relativePath || 'unknown-file';
    const header = lines.length > 1
        ? `${path}:${startLine}-${startLine + lines.length - 1}`
        : `${path}:${startLine}`;
    const body = lines.map((text, i) => `${startLine + i}: ${text}`).join('\n');
    return `${header}\n${body}`;
}

/**
 * Full prefilled task description for a gutter line selection: two leading
 * newlines (a blank space above the reference — the caret starts on top so
 * the description is typed above the code block), then the reference block.
 */
export function buildLineSelectionPrompt(sel: { relativePath: string; startLine: number; lines: string[] }): string {
    return '\n\n' + formatLineSelection(sel.relativePath, sel.startLine, sel.lines);
}
