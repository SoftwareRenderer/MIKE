/**
 * The split a chat message body renders: prose runs and fenced code. It is `marked`'s own grammar, so the
 * prose between two fences returns as one run and a fence cannot move the paragraphs around it. Only a
 * top-level code token is split out: a fence inside a list item or blockquote sits inside the token that
 * contains it, and reaching it means re-parsing someone else's token.
 */
import { Marked, type Tokens } from 'marked';
import { fenceTag, isRenderedFence } from './sanitize';

export type MarkdownSegment =
    | { kind: 'markdown'; source: string }
    | { kind: 'code'; lang: string; text: string; open: boolean };

const lexer = new Marked({ async: false });

/** Where a fenced block starts: up to three spaces of indent, then three or more of one marker character. */
const FENCE_OPEN = /^[ ]{0,3}(`{3,}|~{3,})/;

/** Where a fenced block ends: the marker line and nothing on it but spaces. */
const FENCE_CLOSE = /^[ ]{0,3}(`{3,}|~{3,})[ \t]*$/;

/**
 * Whether a code token's fence is still open — the shape a streaming answer ends in. `marked` records no
 * field for it, so the source is read by the grammar's own rule: a closing line repeats the opening
 * marker character, at the same length or longer.
 */
function fenceIsOpen(raw: string): boolean {
    const lines = raw.trimEnd().split('\n');
    const opening = FENCE_OPEN.exec(lines[0] ?? '');
    if (!opening) return false;
    const closing = lines.length > 1 ? FENCE_CLOSE.exec(lines[lines.length - 1]) : null;
    return !closing || closing[1][0] !== opening[1][0] || closing[1].length < opening[1].length;
}

/** The message as segments, in source order — each one a verbatim slice of it. */
export function splitMarkdownSegments(markdown: string): MarkdownSegment[] {
    const segments: MarkdownSegment[] = [];
    let prose = '';
    const flush = () => {
        // A run of nothing but the separators between blocks renders as nothing.
        if (prose.trim()) segments.push({ kind: 'markdown', source: prose });
        prose = '';
    };
    for (const token of lexer.lexer(markdown)) {
        // A fence with no body is a control with nothing to copy, so the prose parse keeps its block.
        const fence = token.type === 'code' ? (token as Tokens.Code) : null;
        if (fence && fence.text.trim() && !isRenderedFence(fence.lang)) {
            flush();
            segments.push({
                kind: 'code',
                lang: fenceTag(fence.lang),
                text: fence.text,
                open: fenceIsOpen(token.raw),
            });
            continue;
        }
        prose += token.raw;
    }
    flush();
    return segments;
}
