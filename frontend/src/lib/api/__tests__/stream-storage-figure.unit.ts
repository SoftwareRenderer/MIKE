/**
 * The Done column's task figure arrives on a WebSocket frame, and this dispatcher decides whether it reaches
 * the store. A test that writes to the store directly would not notice it being dropped, so these send a
 * real frame down the socket.
 */
import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { createAppStream, type AppStream } from '../stream';
import { storageTasksSentence } from '$lib/utils/storage-usage';
import type { StorageUsage, TaskSpaceFigure } from '$lib/types';

class FakeSocket {
    static instances: FakeSocket[] = [];
    static CONNECTING = 0;
    static OPEN = 1;
    static CLOSED = 3;

    readyState = FakeSocket.CONNECTING;
    onopen: (() => void) | null = null;
    onmessage: ((e: { data: string }) => void) | null = null;
    onclose: (() => void) | null = null;
    onerror: ((e: unknown) => void) | null = null;

    constructor(public url: string) { FakeSocket.instances.push(this); }
    send() {}
    close() { this.readyState = FakeSocket.CLOSED; this.onclose?.(); }
    open() { this.readyState = FakeSocket.OPEN; this.onopen?.(); }
    receive(frame: Record<string, any>) { this.onmessage?.({ data: JSON.stringify(frame) }); }
}

let stream: AppStream | null = null;
let realWS: any;

/** Opens a stream and collects both shapes carried under the one frame name. */
function openStream(): {
    socket: FakeSocket;
    readings: StorageUsage[];
    figures: { projectId: string; figure: TaskSpaceFigure }[];
} {
    const readings: StorageUsage[] = [];
    const figures: { projectId: string; figure: TaskSpaceFigure }[] = [];
    stream = createAppStream({
        onStorageUsage: (usage) => { readings.push(usage); },
        onTaskSpace: (projectId, figure) => { figures.push({ projectId, figure }); },
    });
    const socket = FakeSocket.instances[FakeSocket.instances.length - 1];
    socket.open();
    return { socket, readings, figures };
}

beforeEach(() => {
    FakeSocket.instances = [];
    stream = null;
    realWS = (globalThis as any).WebSocket;
    (globalThis as any).WebSocket = FakeSocket;
});

afterEach(() => {
    stream?.close();
    stream = null;
    (globalThis as any).WebSocket = realWS;
});

describe('the task figure on the storage_usage frame', () => {
    it('reaches the consumer that draws the tooltip line, named for the project it measured', () => {
        const { socket, figures } = openStream();
        socket.receive({ type: 'storage_usage', project_id: 'p1', tasks_bytes: 430, tasks_count: 3 });

        expect(figures).toHaveLength(1);
        expect(figures[0].projectId).toBe('p1');
        expect(figures[0].figure).toEqual({ tasks_bytes: 430, tasks_count: 3 });
        expect(storageTasksSentence(figures[0].figure)).toBeTruthy();
    });

    it('is one frame per project, so two boards keep two figures', () => {
        const { socket, figures } = openStream();
        socket.receive({ type: 'storage_usage', project_id: 'p1', tasks_bytes: 430, tasks_count: 3 });
        socket.receive({ type: 'storage_usage', project_id: 'p2', tasks_bytes: 12, tasks_count: 1 });

        expect(figures.map((f) => f.projectId)).toEqual(['p1', 'p2']);
    });

    it('is absent on a reading taken before the walk finished, so no line is drawn for it', () => {
        const { socket, figures } = openStream();
        socket.receive({ type: 'storage_usage', project_id: 'p1', tasks_bytes: 430 });
        socket.receive({ type: 'storage_usage', project_id: 'p1', tasks_count: 3 });

        expect(figures).toHaveLength(0);
        expect(storageTasksSentence(null)).toBeNull();
    });

    it('never arrives as a filesystem reading, which the tooltip would divide by', () => {
        const { socket, readings, figures } = openStream();
        socket.receive({ type: 'storage_usage', project_id: 'p1', tasks_bytes: 430, tasks_count: 3 });

        expect(figures).toHaveLength(1);
        expect(readings).toHaveLength(0);
    });

    it('never arrives as a figure for the board on screen', () => {
        const { socket, readings, figures } = openStream();
        socket.receive({ type: 'storage_usage', total_bytes: 1024, avail_bytes: 594 });

        expect(readings).toHaveLength(1);
        expect(readings[0]).toMatchObject({ total_bytes: 1024, avail_bytes: 594 });
        expect(figures).toHaveLength(0);
    });
});
