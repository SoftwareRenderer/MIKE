/**
 * Shared types for the capability-confined extension host.
 */

export const INJECTION_POINTS = {
    TASK_CARD: 'taskCard',
    FILE_EDITOR: 'fileEditor',
    PROJECT_BOARD: 'projectBoard',
    TASK_CHAT: 'taskChat',
    PROJECT_SIDEBAR: 'projectSidebar',
    FILE_BROWSER: 'fileBrowser',
    CREATE_TASK: 'createTask',
    /**
     * The body of the Preview a binary file gets when an extension declared it renders that file, the
     * affordance beside an image's "View image". The mount context names the file the frame may read.
     */
    BINARY_PREVIEW: 'binaryPreview',
} as const;

export type InjectionPoint = (typeof INJECTION_POINTS)[keyof typeof INJECTION_POINTS];

export interface InjectionContext {
    component: string;
    taskId?: string;
    projectId?: string;
    filePath?: string;
    [key: string]: any;
}

type CapabilityKind = 'call' | 'event';

type CapabilityHandler = (ctx: HostContext, args: any) => any | Promise<any>;

export interface HostContext {
    extensionId: string;
    extensionName: string;
    point: InjectionPoint;
    config: any;
    context: InjectionContext;
    anchor: HTMLElement | null;
    sendEvent: (cap: string, data: any) => void;
    ui: {
        setLoading: (message: string) => void;
        setError: (message: string) => void;
        hide: () => void;
        setSize: (height: number) => void;
        /** Size the iframe to fill its anchor (which fills the editor content
         * area), so an overlay tracks editor/panel resizes automatically. */
        setFill: () => void;
    };
}

export interface CapabilityDef {
    kind: CapabilityKind;
    argsSchema?: Record<string, 'string' | 'number' | 'boolean' | 'object' | 'array' | 'any'>;
    handler?: CapabilityHandler;
    onGrant?: (ctx: HostContext) => (() => void) | void;
}

export interface HostRequest {
    type: 'call';
    id: string;
    cap: string;
    args?: any;
}

/**
 * Channel handshake (document identity). The iframe's frame is not a trust boundary: a
 * sandboxed frame can navigate itself anywhere and the sandbox flags follow the document,
 * so `ev.origin === 'null'` + `ev.source` prove only "that frame sent this". The handshake
 * binds the channel to the document the host mounted: a one-time token in the iframe URL,
 * echoed back from `location.search`. A document that arrived any other way gets no `init`,
 * no pushed events and no capability dispatch.
 */
export interface HostHello {
    type: 'exthello';
    token: string;
}

export type HostMessage =
    | { type: 'init'; extensionId: string; extensionName: string; point: InjectionPoint; granted: string[]; config: any; context: InjectionContext }
    | { type: 'response'; id: string; ok: true; data?: any }
    | { type: 'response'; id: string; ok: false; error: string }
    | { type: 'event'; cap: string; data: any }
    | HostHello
    | { type: 'exthello-request' };

/**
 * One file's bytes, answered outside `HostMessage` because it cannot survive it: the JSON round-trip
 * that strips Svelte proxies turns a `Blob` into `{}`. Structured clone carries it by reference instead.
 * The byte direction that matters is this one — a frame reads a file, it cannot post one back.
 */
export interface HostBytesResponse {
    type: 'bytes';
    id: string;
    ok: true;
    /** What the host read, which the extension checks before it parses. */
    byteLength: number;
    blob: Blob;
}
