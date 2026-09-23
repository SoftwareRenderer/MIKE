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
    | { kind: 'code'; lang: string; text: string };

const lexer = new Marked({ async: false });

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
            segments.push({ kind: 'code', lang: fenceTag(fence.lang), text: fence.text });
            continue;
        }
        prose += token.raw;
    }
    flush();
    return segments;
}
