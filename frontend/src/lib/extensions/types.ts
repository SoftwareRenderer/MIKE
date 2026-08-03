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
} as const;

export type InjectionPoint = (typeof INJECTION_POINTS)[keyof typeof INJECTION_POINTS];

export interface InjectionContext {
    component: string;
    taskId?: string;
    projectId?: string;
    filePath?: string;
    [key: string]: any;
}

export type CapabilityKind = 'call' | 'event';

export type CapabilityHandler = (ctx: HostContext, args: any) => any | Promise<any>;

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

export type HostMessage =
    | { type: 'init'; extensionId: string; extensionName: string; point: InjectionPoint; granted: string[]; config: any; context: InjectionContext }
    | { type: 'response'; id: string; ok: true; data?: any }
    | { type: 'response'; id: string; ok: false; error: string }
    | { type: 'event'; cap: string; data: any };
