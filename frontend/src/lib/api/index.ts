// Barrel exports — all public API functions are available via `$lib/api`
// Domain modules are in ./projects, ./tasks, ./messages, etc.

// -- Helpers --
export { generateId, setCsrfToken, clearCsrfToken, ensureCsrfToken } from './helpers';

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
export type { QueuedAction } from './queue';

// -- Projects --
export {
    getProjects,
    getProjectCounts,
    getProjectStats,
    resetProjectStats,
    createProject,
    updateProject,
    buildSandboxImage,
    buildSandboxImageStream,
    deleteProject,
    getBoard,
    reorderTasks,
    isFromCache,
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
    getUntrackedFileContent,
    resetWorktree,
    getGitLogFull,
    getCommitDiff,
    getGitBranches,
    checkoutBranch,
} from './git';

// -- Stream --
export { createAppStream } from './stream';
export type { AppStream, AppStreamCallbacks } from './stream';

// -- Terminal --
export { createTerminal, createProjectTerminal } from './terminal';
