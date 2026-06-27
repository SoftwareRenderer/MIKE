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
    __debugOfflineStatus,
} from '../api';

// -- Types --
export type { QueuedAction } from '../api';

// -- Projects --
export {
    getProjects,
    getProjectCounts,
    getProjectStats,
    resetProjectStats,
    createProject,
    updateProject,
    buildSandboxImage,
    deleteProject,
    getBoard,
    reorderTasks,
} from '../api';

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
} from '../api';

// -- Messages --
export {
    sendTaskMessage,
    getTaskMessages,
    createTaskMessage,
    deleteTaskMessagesAfter,
    deleteTaskMessage,
    regenerateTaskMessages,
    checkMessageDelete,
    updateTaskMessage,
} from '../api';

// -- Threads --
export {
    getTaskThreads,
    switchTaskThread,
    forkTaskThread,
    deleteTaskThread,
} from '../api';

// -- Config --
export {
    getConfig,
    updateConfig,
    testConnection,
    fetchModels,
    testPiConnection,
} from '../api';

// -- Git --
export {
    getTaskDiff,
    getTaskCommits,
    resetWorktree,
    getGitLogFull,
    getGitBranches,
    checkoutBranch,
} from '../api';

// -- Stream --
export { createAppStream } from './stream';
export type { AppStream, AppStreamCallbacks } from './stream';

// -- Terminal --
export { createTerminal, createProjectTerminal } from './terminal';
