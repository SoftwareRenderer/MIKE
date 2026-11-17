import { describe, it, expect } from 'vitest';
import { readEventStream, isAborted, type EventFrame } from '../helpers';
import { streamBody } from './fake-stream-body';

async function collect(res: Response): Promise<EventFrame[]> {
    const frames: EventFrame[] = [];
    for await (const frame of readEventStream(res)) frames.push(frame);
    return frames;
}

describe('readEventStream', () => {
    it('yields a frame whose bytes arrived in two chunks', async () => {
        const { res } = streamBody(['data: {"a":1}\n', '\ndata: {"b":2}\n\n']);
        expect(await collect(res)).toEqual([
            { event: 'message', data: '{"a":1}' },
            { event: 'message', data: '{"b":2}' },
        ]);
    });

    it('drops the frame the stream ended in the middle of', async () => {
        // A half-written frame is not a frame: the caller that needs a terminal one reports the
        // early end, and a caller that does not must not see a payload it cannot trust.
        const { res } = streamBody(['data: {"a":1}\n\n', 'data: {"b"']);
        expect(await collect(res)).toEqual([{ event: 'message', data: '{"a":1}' }]);
    });

    it('hands a non-JSON data line to the caller unchanged', async () => {
        // Build-log output is plain text, and one unparseable line must not end the stream.
        const { res } = streamBody(['data: Step 3/7 RUN make build\n\n', 'data: {"status":"built"}\n\n']);
        const frames = await collect(res);
        expect(frames.map((f) => f.data)).toEqual(['Step 3/7 RUN make build', '{"status":"built"}']);
        expect(frames[0].event).toBe('message');
    });

    it('strips the one space after the field name and no more', async () => {
        const { res } = streamBody(['data:    indented\n\n', 'data:{"compact":1}\n\n']);
        expect((await collect(res)).map((f) => f.data)).toEqual(['   indented', '{"compact":1}']);
    });

    it('reads the event name and defaults to message', async () => {
        const { res } = streamBody([
            'event: token\ndata: {"content":"hel"}\n\n',
            'event:done\ndata: {"content":"hello"}\n\n',
            'data: {"content":"and a bare one"}\n\n',
        ]);
        expect(await collect(res)).toEqual([
            { event: 'token', data: '{"content":"hel"}' },
            { event: 'done', data: '{"content":"hello"}' },
            { event: 'message', data: '{"content":"and a bare one"}' },
        ]);
    });

    it('joins a payload written across two data lines', async () => {
        const { res } = streamBody(['data: {"a":\ndata: 1}\n\n']);
        const [frame] = await collect(res);
        expect(frame.data).toBe('{"a":\n1}');
        expect(JSON.parse(frame.data)).toEqual({ a: 1 });
    });

    it('skips a frame that carries no data field', async () => {
        const { res } = streamBody([': keep-alive\n\n', 'data: {"a":1}\n\n']);
        expect(await collect(res)).toEqual([{ event: 'message', data: '{"a":1}' }]);
    });

    it('refuses a response with no body to stream', async () => {
        await expect(collect({ body: null } as unknown as Response))
            .rejects.toThrow('Streaming not supported');
    });

    it('releases the lock when the consumer stops early', async () => {
        const { res, released } = streamBody(['data: {"a":1}\n\n', 'data: {"b":2}\n\n']);
        for await (const frame of readEventStream(res)) {
            expect(frame.data).toBe('{"a":1}');
            break;
        }
        expect(released).toEqual(['released']);
    });

    it('releases the lock when a read fails, and reports the failure unchanged', async () => {
        // A cancelled request reaches the caller as the cancellation it is — never wrapped, so
        // `isAborted` still answers and the app stays online.
        const cancelled = Object.assign(new Error('signal aborted'), { name: 'AbortError' });
        const { res, released } = streamBody(['data: {"a":1}\n\n', cancelled]);
        let caught: unknown = null;
        try {
            await collect(res);
        } catch (e) {
            caught = e;
        }
        expect(caught).toBe(cancelled);
        expect(isAborted(caught)).toBe(true);
        expect(released).toEqual(['released']);
    });
});
