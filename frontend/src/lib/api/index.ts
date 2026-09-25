// Barrel exports — the domain functions are reachable via `$lib/api`; domain modules live
// in ./projects, ./tasks, ./git, etc.
//
// The fetch primitives under this barrel (`apiFetch`, `apiError`, `API_BASE`, `isAborted`) are not
// re-exported, so a component cannot hand-roll a fetch.

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
    getQueuedSends,
    getRefusedSends,
    getDroppedTaskIds,
    getDroppedActions,
    flushQueueNow,
    waitForFlush,
    applyQueuedActionsToBoard,
} from './queue';

// The send types are declared with the send call itself, which the queue replays.
export type { SendResult, SendTaskMessagePayload } from './raw/tasks';

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
    deleteProject,
    getBoard,
} from './projects';

// A sandbox build is a progress stream, never a queued write, so it lives under ./raw/.
export { buildSandboxImageStream } from './raw/projects';

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
    getTaskTurns,
    getTaskMessageFull,
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
    getProjectDiffFile,
    toDiffFiles,
    getGitBranches,
    checkoutBranch,
    getGitStatus,
    stageProjectFile,
    commitProject,
    discardProjectFile,
    pullProject,
    pushProject,
    getTaskGitLog,
    getTaskCommitDiffFiles,
} from './git';
export type { ParsedDiffFile, TaskDiff, TaskDiffOptions } from './git';

// -- Stream --
export { createAppStream } from './stream';
export type { AppStream } from './stream';

// -- Terminal --
export { createTerminal, createProjectTerminal, isTerminalStale } from './terminal';

// -- Pasted/dropped files written into the task's own environment --
export { uploadTaskFile } from './task-upload';

// -- A file's own bytes, for an `<img>` src --
export { rawFileURL, listFsDirectory } from './fs';
export type { FsEntry } from './fs';
export { deleteFsPaths, copyFsPaths, moveFsPaths, renameFsPath } from './fs-ops';
export type { FsOpsResult } from './fs-ops';

// -- Auth (the session check, the login and the logout that gate the whole app) --
export { checkSession, login, logout } from './auth';

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
