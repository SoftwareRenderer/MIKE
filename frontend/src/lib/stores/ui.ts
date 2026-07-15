// Legacy compat — re-export from reactive store
// Prefer importing from $lib/stores (appStore, uiStore, dialogStore)
import { uiStore } from './index';

export const showCreateTask = {
    get: () => uiStore.showCreateTask,
    set: (v: typeof uiStore.showCreateTask) => { uiStore.showCreateTask = v; },
    subscribe: (fn: (v: typeof uiStore.showCreateTask) => void) => { fn(uiStore.showCreateTask); return () => {}; },
} as any;

export const creatingChildOf = {
    get: () => uiStore.creatingChildOf,
    set: (v: typeof uiStore.creatingChildOf) => { uiStore.creatingChildOf = v; },
    subscribe: (fn: (v: typeof uiStore.creatingChildOf) => void) => { fn(uiStore.creatingChildOf); return () => {}; },
} as any;

export const showConfig = {
    get: () => uiStore.showConfig,
    set: (v: typeof uiStore.showConfig) => { uiStore.showConfig = v; },
    subscribe: (fn: (v: typeof uiStore.showConfig) => void) => { fn(uiStore.showConfig); return () => {}; },
} as any;

export const showTerminal = {
    get: () => uiStore.showTerminal,
    set: (v: typeof uiStore.showTerminal) => { uiStore.showTerminal = v; },
    subscribe: (fn: (v: typeof uiStore.showTerminal) => void) => { fn(uiStore.showTerminal); return () => {}; },
} as any;

export const showStats = {
    get: () => uiStore.showStats,
    set: (v: typeof uiStore.showStats) => { uiStore.showStats = v; },
    subscribe: (fn: (v: typeof uiStore.showStats) => void) => { fn(uiStore.showStats); return () => {}; },
} as any;

export const showCardDetail = {
    get: () => uiStore.showCardDetail,
    set: (v: typeof uiStore.showCardDetail) => { uiStore.showCardDetail = v; },
    subscribe: (fn: (v: typeof uiStore.showCardDetail) => void) => { fn(uiStore.showCardDetail); return () => {}; },
} as any;

export const showSandboxConfig = {
    get: () => uiStore.showSandboxConfig,
    set: (v: typeof uiStore.showSandboxConfig) => { uiStore.showSandboxConfig = v; },
    subscribe: (fn: (v: typeof uiStore.showSandboxConfig) => void) => { fn(uiStore.showSandboxConfig); return () => {}; },
} as any;
