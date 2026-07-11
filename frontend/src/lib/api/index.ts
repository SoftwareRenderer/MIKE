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
    getQueueLength,
    flushQueueNow,
    waitForFlush,
    applyQueuedActionsToBoard,
    __debugOfflineStatus,
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
    buildSandboxImage,
    buildSandboxImageStream,
    validateForwardPorts,
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
    startTask,
    startSubtask,
    stopTask,
    resumeTask,
    summarizeTask,
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
    testPiConnection,
    reloadPrompts,
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
} from './git';

// -- Stream --
export { createAppStream } from './stream';
export type { AppStream, AppStreamCallbacks } from './stream';

// -- Terminal --
export { createTerminal, createProjectTerminal, isTerminalStale } from './terminal';
