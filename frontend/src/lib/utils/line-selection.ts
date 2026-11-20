/**
 * Pure helpers for the "select lines in the editor gutter → create task"
 * flow (gutter clicks in the Explorer file editors; multi-line selections
 * come from the editor's own text selection).
 *
 * No CodeMirror or DOM dependencies — unit tested in
 * `__tests__/line-selection.unit.ts`.
 */

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
 * The path this file's tree knows it by — what `GET /api/fs/raw` reads and what a task description header
 * names.
 *
 * The editor holds the path its listing handed over: the host path for a plain project, the container mount
 * `/home/sandbox/<name>/...` for a sandboxed one. The host passes both roots it knows — the checkout, and the
 * mount its listing navigates by, which the project row names and the folder need not. Nothing is guessed from
 * a folder name: a host path under an unrelated `/home/sandbox/...` must not strip. Anything unrecognized is
 * returned as-is, so a description still carries a usable path.
 */
export function toProjectRelative(filePath: string, hostRoot: string, containerRoot = ''): string {
    if (!filePath) return '';
    for (const root of [hostRoot, containerRoot]) {
        const cut = (root || '').replace(/\/+$/, '');
        if (cut && filePath.startsWith(cut + '/')) return filePath.slice(cut.length + 1);
    }
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
