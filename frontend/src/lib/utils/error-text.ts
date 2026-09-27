/**
 * A request failure as one line of text: its `message` alone, because `String(e)` prints the JS class name
 * in front of it, and `fallback` when it has none. Never wrap the result in `t()` — a message is a wire value.
 */
export function errorText(e: unknown, fallback = ''): string {
    const text = e instanceof Error ? e.message : e === null || e === undefined ? '' : String(e);
    return text.trim() === '' ? fallback : text;
}
