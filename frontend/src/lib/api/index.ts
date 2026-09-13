// Barrel exports — all public API functions are available via `$lib/api`
// Domain modules are in ./projects, ./tasks, ./messages, etc.

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

// -- Types --
export type { QueuedAction, ActionType } from './queue';

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
    regenerateTaskMessages,
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
    testConnection,
    fetchModels,
} from './config';

// -- Git --
export {
    getTaskDiff,
    getTaskCommits,
    getDiffFile,
    stageFile,
    getGitLogFull,
    getCommitDiffFiles,
    getProjectDiffFiles,
    toDiffFiles,
    getGitBranches,
    checkoutBranch,
    getGitStatus,
    stageProjectFile,
    commitProject,
    getTaskGitLog,
    getTaskCommitDiffFiles,
} from './git';

// -- Stream --
export { createAppStream } from './stream';
export type { AppStream, AppStreamCallbacks } from './stream';

// -- Terminal --
export { createTerminal, createProjectTerminal, isTerminalStale } from './terminal';

// -- LSP --
export { fetchLSPSandboxMemory } from './lsp';

// -- Pasted/dropped files written into the task's own environment --
export { uploadTaskFile } from './task-upload';

// -- Sessions (login sessions, Settings → About) --
export {
    listSessions,
    revokeSession,
    revokeOtherSessions,
} from './sessions';

// -- Extensions --
export {
    listExtensions,
    listLocalExtensions,
    updateExtension,
    updateExtensionImage,
    deleteExtension,
    setExtensionEnabled,
    setDependencyCache,
    getHomeDirectory,
    getRegistryIndex,
    installExtension,
    installLocalExtension,
    checkUpdates,
} from './extensions';
