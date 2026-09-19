// Barrel exports — the domain functions are reachable via `$lib/api`; domain modules live
// in ./projects, ./tasks, ./git, etc.
//
// The fetch primitives underneath the barrel (`apiFetch`, `apiError`, `API_BASE`, `isAborted`)
// are deliberately not re-exported: adding them would invite a component to hand-roll a fetch,
// which is the opposite of what this barrel is for. No component imports them; the deep
// importers are inside `$lib/api` itself, the extension handlers, and the two services that
// own a request lifecycle (`services/app-update.ts`, `services/diff-cache.svelte.ts`).

// -- Helpers --
export { generateId, setCsrfToken, clearCsrfToken, ensureCsrfToken } from './helpers';

// -- Cache --
export { isFromCache } from './cache';

// -- Queue --
export {
    getOfflineStatus,
    setOfflineStatus,
    onQueueChanged,
    onQueueFlushed,
    loadQueue,
    clearQueue,
    enqueueAction,
    isTransientError,
    getQueuedTaskIds,
    getQueuedDeleteTaskIds,
    getDroppedTaskIds,
    getDroppedActions,
    flushQueueNow,
    waitForFlush,
    applyQueuedActionsToBoard,
} from './queue';

// -- Projects --
export {
    getProject,
    getProjects,
    getProjectCounts,
    getProjectStats,
    resetProjectStats,
    createProject,
    cloneProject,
    updateProject,
    buildSandboxImageStream,
    deleteProject,
    getBoard,
} from './projects';

// -- Tasks --
export {
    createTask,
    getTask,
    updateTask,
    deleteTask,
    moveTask,
    dismissMemoryAlert,
    commitAndDone,
    clearDoneTasks,
    initTask,
    startTask,
    startSubtask,
    stopTask,
    resumeTask,
    summarizeTask,
    compactTask,
} from './tasks';

// -- Messages --
export {
    sendTaskMessage,
    getTaskMessages,
    getTaskMessageFull,
    createTaskMessage,
    deleteTaskMessagesAfter,
    deleteTaskMessage,
    checkMessageDelete,
    updateTaskMessage,
} from './tasks';

// -- Threads --
export {
    getTaskThreads,
    switchTaskThread,
    forkTaskThread,
} from './threads';

// -- Config --
export {
    getConfig,
    updateConfig,
    setUpdateCheckConsent,
    testConnection,
    fetchModels,
} from './config';

// -- Release updates (Settings → About; docs/auto-update.md) --
export {
    fetchUpdateStatus,
    checkForUpdates,
    prepareUpdate,
    applyUpdate,
    restartToUpdate,
} from './updates';

// -- Git --
export {
    getTaskDiff,
    getTaskCommits,
    getDiffFile,
    stageFile,
    getGitLogFull,
    getCommitDiffFiles,
    getProjectDiffFiles,
    getProjectDiffFile,
    toDiffFiles,
    getGitBranches,
    checkoutBranch,
    getGitStatus,
    stageProjectFile,
    commitProject,
    pullProject,
    pushProject,
    getTaskGitLog,
    getTaskCommitDiffFiles,
} from './git';
export type { ParsedDiffFile } from './git';

// -- Stream --
export { createAppStream } from './stream';
export type { AppStream } from './stream';

// -- Terminal --
export { createTerminal, createProjectTerminal, isTerminalStale } from './terminal';

// -- LSP --
export { fetchLSPSandboxMemory } from './lsp';

// -- Pasted/dropped files written into the task's own environment --
export { uploadTaskFile } from './task-upload';

// -- A file's own bytes, for an `<img>` src --
export { rawFileURL } from './fs';

// -- Sessions (login sessions, Settings → About) --
export {
    listSessions,
    revokeSession,
    revokeOtherSessions,
} from './sessions';

// -- API tokens (bearer credentials, Settings → About) --
export {
    listAPITokens,
    createAPIToken,
    revokeAPIToken,
} from './tokens';

// -- Extensions --
export {
    listExtensions,
    listLocalExtensions,
    updateExtension,
    updateExtensionImage,
    deleteExtension,
    setExtensionEnabled,
    setDependencyCache,
    setAgentMount,
    getHomeDirectory,
    getRegistryIndex,
    installExtension,
    installLocalExtension,
    checkUpdates,
    testExtensionUrl,
} from './extensions';

export type { ExtensionUrlProbe } from './extensions';
