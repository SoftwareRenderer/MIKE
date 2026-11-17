/**
 * A `Response` whose body hands the given chunks to a reader, for the tests that read a server
 * progress stream (`readEventStream` and its callers).
 *
 * A chunk that is an `Error` makes the read reject with it — how an aborted request reaches the reader.
 * Chunks are cut wherever the test names them, so a frame can straddle a chunk edge, which is the
 * boundary a stream reader must not treat as one.
 *
 * `released` counts the `releaseLock` calls, since that is the whole of what a reader owes the stream it
 * was handed.
 */
export function streamBody(chunks: (string | Error)[]): { res: Response; released: string[] } {
    const encoder = new TextEncoder();
    let next = 0;
    const released: string[] = [];
    const res = {
        ok: true,
        body: {
            getReader: () => ({
                read: async () => {
                    if (next >= chunks.length) return { done: true, value: undefined };
                    const chunk = chunks[next++];
                    if (chunk instanceof Error) throw chunk;
                    return { done: false, value: encoder.encode(chunk) };
                },
                releaseLock: () => { released.push('released'); },
            }),
        },
    } as unknown as Response;
    return { res, released };
}

/** The same body, from whole `data:` frames — each frame written the way the Go side writes it. */
export function streamFrames(frames: string[]): Response {
    return streamBody([frames.map(f => `data: ${f}\n\n`).join('')]).res;
}
