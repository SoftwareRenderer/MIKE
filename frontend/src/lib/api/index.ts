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
    enqueueAction,
    getQueuedTaskIds,
    getQueuedDeleteTaskIds,
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
    updateProject,
    buildSandboxImageStream,
    deleteProject,
    getBoard,
    reorderTasks,
} from './projects';

// -- Tasks --
export {
    createTask,
    getTask,
    updateTask,
    deleteTask,
    moveTask,
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
    deleteTaskThread,
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
    resetWorktree,
    stageFile,
    getGitLogFull,
    getCommitDiff,
    getGitBranches,
    checkoutBranch,
    getGitStatus,
    getProjectDiff,
    stageProjectFile,
    commitProject,
    getTaskGitLog,
    getTaskGitBranches,
    checkoutTaskBranch,
    getTaskCommitDiff,
} from './git';

// -- Stream --
export { createAppStream } from './stream';
export type { AppStream, AppStreamCallbacks } from './stream';

// -- Terminal --
export { createTerminal, createProjectTerminal, isTerminalStale } from './terminal';

// -- Extensions --
export {
    listExtensions,
    getExtension,
    createExtension,
    updateExtension,
    updateExtensionImage,
    deleteExtension,
    setExtensionEnabled,
    getRegistryIndex,
    installExtension,
    checkUpdates,
    getExtensionLogs,
    streamBuildProgress,
    type BuildProgressEvent,
} from './extensions';
