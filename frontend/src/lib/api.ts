// API module — re-exports from domain modules
// Domain modules: ./api/queue.ts, ./api/projects.ts, ./api/tasks.ts, ./api/config.ts, ./api/git.ts, ./api/threads.ts

import type { Board, Config, GitCommit, Project, ProjectCounts, ProjectStats, ReorderRequest, Task, TaskCommit, TaskMessage, Thread } from './types';
import { setCsrfToken, clearCsrfToken, ensureCsrfToken } from './api/helpers';

// Re-export helpers for direct imports from $lib/api
export { setCsrfToken, clearCsrfToken, ensureCsrfToken };

// Re-export stream and terminal modules
export { createAppStream } from './api/stream';
export type { AppStream, AppStreamCallbacks } from './api/stream';
export { createTerminal, createProjectTerminal } from './api/terminal';

// Re-export all domain modules
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
    type QueuedAction,
    generateId,
} from './api/queue';

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
} from './api/projects';

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
    sendTaskMessage,
    getTaskMessages,
    getTaskMessageFull,
    createTaskMessage,
    deleteTaskMessagesAfter,
    deleteTaskMessage,
    regenerateTaskMessages,
    checkMessageDelete,
    updateTaskMessage,
} from './api/tasks';

export {
    getTaskDiff,
    getTaskCommits,
    getDiffFile,
    getUntrackedFileContent,
    resetWorktree,
} from './api/git';

export {
    getConfig,
    updateConfig,
    testConnection,
    fetchModels,
    testPiConnection,
    reloadPrompts,
} from './api/config';

export {
    getGitLogFull,
    getCommitDiff,
    getGitBranches,
    checkoutBranch,
} from './api/git';

export {
    getTaskThreads,
    switchTaskThread,
    forkTaskThread,
    deleteTaskThread,
} from './api/threads';
