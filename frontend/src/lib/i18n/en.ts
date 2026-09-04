/**
 * The English catalog of record (T5).
 *
 * Flat dotted keys, namespace-prefixed (`sandbox.…`, `terminal.…`, `stats.…`, `api.…`). English values are
 * the literal strings the UI used to carry inline — byte-identical, `aria-label`/`title`/`placeholder`
 * included, so a language switch is the only thing that ever changes what a user reads.
 *
 * Interpolation is **named**: `'Remove {name} from {column}?'` with `t('key', { name, column })`. A
 * placeholder is `{name}` (one identifier, braces only around it); an unquoted `{` in the text is left
 * alone, so literal braces survive a round trip.
 *
 * Plurals are a nested object and are selected from `params.count` by `Intl.PluralRules` — never by an
 * inline `n === 1 ? '' : 's'` at a call site:
 *
 *     'stats.tokens': { one: '{count} token', other: '{count} tokens' }
 *
 * Keys are grouped by the batch that owns them; do not rename another area's namespace, and never add a
 * second key for wording that already exists in it.
 */

/** A translatable string, or the per-plural-category forms of one. */
export type PluralCategories = {
    zero?: string;
    one?: string;
    two?: string;
    few?: string;
    many?: string;
    other: string;
};

export type MessageValue = string | PluralCategories;

/** One locale's catalog: every key maps to a string or a plural object. */
export type Catalog = Record<string, MessageValue>;

export const en = {
    // ─── api/* — fallback text for a failed request ───────────────────────────────
    // The server's own `body.error` always wins when present; these are what the browser
    // shows when the response carried no message. Never wrap server text in `t()`.
    'api.fetchConfig': 'Failed to fetch config',
    'api.testConnection': 'Failed to test connection',
    'api.fetchModels': 'Failed to fetch models',
    'api.fetchExtensions': 'Failed to fetch extensions',
    'api.updateExtension': 'Failed to update extension',
    'api.deleteExtension': 'Failed to delete extension',
    'api.updateExtensionEnabledState': 'Failed to update extension enabled state',
    'api.updateDependencyDirectory': 'Failed to update the dependency directory',
    'api.readHomeDirectory': 'Failed to read the home directory',
    'api.fetchLocalExtensions': 'Failed to fetch local extensions',
    'api.fetchRegistry': 'Failed to fetch registry',
    'api.checkForUpdates': 'Failed to check for updates',
    'api.operationFailed': 'Operation failed',
    'api.unexpectedResponse': 'Unexpected response from {path} ({contentType}): {body}',
    'api.emptyBody': 'empty body',
    'api.noReader': 'No reader available',
    'api.operationEndedEarly': 'Operation ended before reporting completion',
    'api.fetchGitLog': 'Failed to fetch git log',
    'api.fetchCommitDiff': 'Failed to fetch commit diff',
    'api.fetchBranches': 'Failed to fetch branches',
    'api.checkoutBranch': 'Failed to checkout branch',
    'api.fetchGitStatus': 'Failed to fetch git status',
    'api.fetchProjectDiff': 'Failed to fetch project diff',
    'api.fetchFile': 'Failed to fetch file',
    'api.stageFile': 'Failed to stage file',
    'api.unstageFile': 'Failed to unstage file',
    'api.commit': 'Failed to commit',
    'api.pull': 'Failed to pull',
    'api.push': 'Failed to push',
    'api.fetchDiff': 'Failed to fetch diff',
    'api.fetchCommits': 'Failed to fetch commits',
    'api.fetchFileContent': 'Failed to fetch file content',
    'api.fetchTaskGitLog': 'Failed to fetch task git log',
    'api.fetchTaskCommitDiff': 'Failed to fetch task commit diff',
    'api.createProject': 'Failed to create project',
    'api.updateProject': 'Failed to update project',
    'api.buildSandboxImage': 'Failed to build sandbox image',
    'api.streamingNotSupported': 'Streaming not supported',
    'api.buildCompletedWithoutResult': 'Build completed without result',
    'api.deleteProject': 'Failed to delete project',
    'api.resetStats': 'Failed to reset stats',
    'api.createTask': 'Failed to create task',
    'api.updateTask': 'Failed to update task',
    'api.deleteTask': 'Failed to delete task',
    'api.moveTask': 'Failed to move task',
    'api.commitAndDone': 'Failed to commit and mark as done',
    'api.clearDoneTasks': 'Failed to clear done tasks',
    'api.initializeTask': 'Failed to initialize task',
    'api.startTask': 'Failed to start task',
    'api.startSubtask': 'Failed to start subtask',
    'api.stopTask': 'Failed to stop task',
    'api.retryTask': 'Failed to retry task',
    'api.resumeTask': 'Failed to resume task',
    'api.sendMessage': 'Failed to send message',
    'api.createMessage': 'Failed to create message',
    'api.deleteMessages': 'Failed to delete messages',
    'api.deleteMessage': 'Failed to delete message',
    'api.updateMessage': 'Failed to update message',
    'api.updateConfig': 'Failed to update config',
    'api.switchThread': 'Failed to switch thread',
    'api.forkThread': 'Failed to fork thread',
    'api.fetchProjects': 'Failed to fetch projects',
    'api.fetchProject': 'Failed to fetch project',
    'api.fetchProjectCounts': 'Failed to fetch project counts',
    'api.fetchProjectStats': 'Failed to fetch project stats',
    'api.cloneProject': 'Failed to clone project',
    'api.cloneCompletedWithoutResult': 'Clone completed without result',
    'api.fetchBoard': 'Failed to fetch board',
    'api.taskNotFound': 'Task not found',
    'api.fetchMessages': 'Failed to fetch messages',
    'api.fetchMessage': 'Failed to fetch message',
    'api.checkMessageDelete': 'Failed to check message delete',
    'api.summarizeTask': 'Failed to summarize task',
    'api.compactTask': 'Failed to compact task',
    'api.fetchSessions': 'Failed to fetch sessions',
    'api.revokeSession': 'Failed to revoke session',
    'api.revokeOtherSessions': 'Failed to revoke other sessions',
    'api.fetchThreads': 'Failed to fetch threads',
    'api.loadExtensions': 'Failed to load extensions',
    'api.loadRegistry': 'Failed to load registry',
    'api.installExtension': 'Failed to install extension',

    // ─── sandbox/* — Sandbox Configuration modal, Sandbox Info panel, image banner ─────
    // Load-bearing security copy: "Read-only"/"Read/Write" and the disable-sandbox confirmation. Tests
    // match the rendered English (/read-only/). Capability NAMES (CAP_*), container/host paths and port
    // numbers are never catalog entries — only the human description beside a name is. The
    // "set DANGER_ZONE=1 to allow" refusal is the Go server's own message and stays untranslated.
    'sandbox.title': 'Sandbox Configuration',
    'sandbox.lockedNotice': 'Cannot change sandbox settings while tasks are In Progress. The settings below are shown for reference only.',
    'sandbox.enabled': 'Enabled',
    'sandbox.disabled': 'Disabled',
    'sandbox.default': 'Default',
    'sandbox.empty': '(empty)',
    'sandbox.isolation': 'Sandbox Isolation',
    'sandbox.isolationAria': 'Sandbox isolation',
    'sandbox.isolationDesc': 'Use gVisor to isolate all AI actions within a sandbox',
    'sandbox.networkIsolation': 'Network Isolation',
    'sandbox.networkIsolationAria': 'Network isolation',
    'sandbox.networkIsolationDesc': 'Block all network access',
    'sandbox.memoryLimit': 'Memory Limit',
    'sandbox.memoryLimitDesc': 'Max container memory (MB) — blank = no limit',
    'sandbox.memoryLimitAria': 'Maximum container memory in MB',
    'sandbox.noLimit': 'No limit',
    'sandbox.image': 'Image',
    'sandbox.forwardedPorts': 'Forwarded Ports',
    'sandbox.activePortMappings': 'Active Port Mappings',
    'sandbox.customMounts': 'Custom Mounts',
    'sandbox.environmentVariables': 'Environment Variables',
    'sandbox.capabilities': 'Capabilities',
    'sandbox.hostPort': 'Host port',
    'sandbox.containerPort': 'Container port',
    'sandbox.hostPath': 'Host path',
    'sandbox.containerPath': 'Container path',
    'sandbox.readWrite': 'Read/Write',
    'sandbox.readOnly': 'Read-only',
    'sandbox.mountReadWrite': 'Mount Read/Write',
    'sandbox.mountReadOnly': 'Mount Read-only',
    'sandbox.remove': 'Remove',
    'sandbox.cancelEdit': 'Cancel edit',
    'sandbox.cancel': 'Cancel',
    'sandbox.save': 'Save',
    'sandbox.saving': 'Saving...',
    // Forwarded ports
    'sandbox.ports': 'Forward Ports',
    'sandbox.portsAria': 'Toggle port forwarding settings',
    'sandbox.portsDesc': "Host ports are auto-assigned when tasks run, and visible in the task's Sandbox Info panel.",
    'sandbox.portsNoActive': 'Host ports assigned when tasks run',
    'sandbox.addPort': 'Add port forward',
    'sandbox.savePort': 'Save port',
    'sandbox.portRangeError': 'Port must be between 1 and 65535',
    // Custom mounts
    'sandbox.mountsAria': 'Toggle mount settings',
    'sandbox.mountsDesc': 'Bind mount host paths into sandbox containers.',
    'sandbox.addMount': 'Add mount',
    'sandbox.saveMount': 'Save mount',
    'sandbox.browseHostDirectory': 'Browse host directory',
    'sandbox.mountHostPathError': 'Host path is required',
    'sandbox.mountContainerPathError': 'Container path is required',
    // Environment variables
    'sandbox.envsAria': 'Toggle environment variables settings',
    'sandbox.envsDesc': 'Set environment variables in sandbox containers.',
    'sandbox.addVariable': 'Add variable',
    'sandbox.saveVariable': 'Save variable',
    'sandbox.envNameAria': 'Variable name',
    'sandbox.envValueAria': 'Variable value',
    'sandbox.envKeyPlaceholder': 'KEY',
    'sandbox.envValueInputPlaceholder': 'value',
    'sandbox.envNameError': 'Environment variable name is required',
    'sandbox.envDuplicateError': 'Duplicate variable name: {name}',
    // Capabilities
    'sandbox.capsAria': 'Toggle capabilities settings',
    'sandbox.capsDesc': 'Linux capabilities to add to the container. By default, all capabilities are dropped (--cap-drop=ALL).',
    'sandbox.addCapabilityAria': 'Add capability',
    'sandbox.capSuggestionPlaceholder': 'Type capability name, e.g. CAP_NET_RAW',
    'sandbox.capAddMorePlaceholder': 'Add more...',
    'sandbox.removeCapability': 'Remove {cap}',
    // Memory / image source
    'sandbox.imageSource': 'Image Source',
    'sandbox.imageSourceDesc': 'Container image for sandbox',
    'sandbox.imagePrebuilt': 'Pre-built',
    'sandbox.imageName': 'Image Name',
    'sandbox.imageNameDesc': 'Image for sandbox containers — must name its registry, e.g. docker.io/library/ubuntu:24.04',
    'sandbox.imageNameAria': 'Sandbox image name',
    'sandbox.dockerfile': 'Dockerfile',
    'sandbox.dockerfileDesc': 'Select a Dockerfile on the server',
    'sandbox.disableCache': 'Disable cache',
    'sandbox.skipCacheTitle': 'Skip the container build layer cache for the next build',
    'sandbox.disableCacheAria': 'Disable build cache',
    'sandbox.buildImage': 'Build image',
    'sandbox.build': 'Build',
    'sandbox.building': 'Building...',
    'sandbox.rebuild': 'Rebuild',
    'sandbox.rebuildImage': 'Rebuild image',
    'sandbox.rebuildImageNoCache': 'Rebuild image without cache',
    'sandbox.noDockerfileSelected': 'No Dockerfile selected',
    'sandbox.browse': 'Browse',
    'sandbox.browseDockerfile': 'Browse for Dockerfile',
    'sandbox.buildNotice': 'Build running in background — you can close this dialog.',
    'sandbox.buildOutput': 'Build output',
    'sandbox.imageTagLabel': 'Image tag:',
    'sandbox.saveWarning': 'You have un-added changes — click + to add them before saving.',
    'sandbox.dockerfileRequired': 'Please select a Dockerfile before saving.',
    'sandbox.disableTitle': 'Disable Sandbox',
    'sandbox.disableConfirm': 'Disable sandbox isolation for "{name}"?\n\nTasks will run without gVisor sandboxing, executing commands directly on the host.',
    'sandbox.disableConfirmButton': 'Disable',
    'sandbox.selectDockerfile': 'Select Dockerfile',
    'sandbox.selectDockerfileTitle': 'Select this Dockerfile',
    'sandbox.noDockerfilesFound': 'No Dockerfiles found in this directory',
    'sandbox.selectDirectory': 'Select Directory',
    'sandbox.selectDirectoryTitle': 'Select this directory',
    'sandbox.noSubdirectories': 'No subdirectories in this path',
    // One description per Linux capability name — the NAME is the machine value, never translated.
    'sandbox.cap.CAP_CHOWN': 'Change file ownership',
    'sandbox.cap.CAP_DAC_OVERRIDE': 'Bypass file permission checks',
    'sandbox.cap.CAP_DAC_READ_SEARCH': 'Bypass read permission on files/dirs',
    'sandbox.cap.CAP_FOWNER': 'Bypass permission checks requiring inode owner',
    'sandbox.cap.CAP_FSETID': "Don't clear setuid/setgid on file mod",
    'sandbox.cap.CAP_IPC_LOCK': 'Lock memory',
    'sandbox.cap.CAP_IPC_OWNER': 'Bypass IPC ownership checks',
    'sandbox.cap.CAP_KILL': 'Bypass permission checks for signals',
    'sandbox.cap.CAP_LEASE': 'Lease files',
    'sandbox.cap.CAP_LINUX_IMMUTABLE': 'Set immutable file attribute',
    'sandbox.cap.CAP_MKNOD': 'Create special files',
    'sandbox.cap.CAP_NET_ADMIN': 'Perform network-related operations',
    'sandbox.cap.CAP_NET_BIND_SERVICE': 'Bind to ports < 1024',
    'sandbox.cap.CAP_NET_BROADCAST': 'Make socket broadcasts',
    'sandbox.cap.CAP_NET_RAW': 'Use RAW and PACKET sockets',
    'sandbox.cap.CAP_SETFCAP': 'Set file capabilities',
    'sandbox.cap.CAP_SETGID': 'Manipulate process group IDs',
    'sandbox.cap.CAP_SETPCAP': 'Transfer capabilities',
    'sandbox.cap.CAP_SETUID': 'Manipulate process user IDs',
    'sandbox.cap.CAP_SYS_CHROOT': 'Use chroot()',
    'sandbox.cap.CAP_SYS_PTRACE': 'Trace arbitrary processes',
    'sandbox.cap.CAP_SYS_ADMIN': 'Perform a wide range of system admin operations',
    'sandbox.cap.CAP_SYS_BOOT': 'Reboot the system',
    'sandbox.cap.CAP_SYS_MODULE': 'Load/unload kernel modules',
    'sandbox.cap.CAP_SYS_NICE': 'Raise process nice priority',
    'sandbox.cap.CAP_SYS_RAWIO': 'Perform raw I/O ports',
    'sandbox.cap.CAP_SYS_RESOURCE': 'Override resource limits',
    'sandbox.cap.CAP_SYS_TIME': 'Set system clock',
    'sandbox.cap.CAP_SYS_TTY_CONFIG': 'Configure TTY devices',
    'sandbox.cap.CAP_BPF': 'Use BPF operations',
    'sandbox.cap.CAP_PERFMON': 'Use performance monitoring',
    'sandbox.cap.CAP_AUDIT_CONTROL': 'Modify kernel audit configuration',
    'sandbox.cap.CAP_AUDIT_READ': 'Read kernel audit log',
    'sandbox.cap.CAP_AUDIT_WRITE': 'Write records to kernel audit log',
    'sandbox.cap.CAP_BLOCK_SUSPEND': 'Use suspend-to-memory/disk',
    'sandbox.cap.CAP_CHECKPOINT_RESTORE': 'Checkpoint and restore',
    'sandbox.cap.CAP_MAC_ADMIN': 'Override MAC configuration',
    'sandbox.cap.CAP_MAC_OVERRIDE': 'Override MAC enforcement',
    'sandbox.cap.CAP_WAKE_ALARM': 'Trigger wake alarms',
    'sandbox.cap.CAP_SYSLOG': 'Perform syslog operations',

    // Sandbox Info panel + the image-preparation banner
    'sandbox.infoTitle': 'Sandbox Info',
    'sandbox.sandboxedTask': 'Sandboxed Task',
    'sandbox.isolationEnabled': 'Sandbox Isolation is enabled for this project',
    'sandbox.memoryUsage': 'Memory Usage',
    'sandbox.peakMemory': 'Peak {mb} MB',
    // The kill count stays marked up in the panel, so the number is not part of the message —
    // `count` only selects the form ("1 process" / "3 processes").
    'sandbox.oomKilled': { one: 'process OOM-killed — memory limit reached', other: 'processes OOM-killed — memory limit reached' },
    'sandbox.loadingSettings': 'Loading settings...',
    'sandbox.portsIdle': 'Container has not started due to idle task or terminal',
    'sandbox.dismiss': 'Dismiss',
    'sandbox.dismissImageError': 'Dismiss sandbox image error',
    'sandbox.imageBuildOutput': 'Image build output',
    'sandbox.imageBuilding': 'Building sandbox image {image}…',
    'sandbox.imagePulling': 'Pulling {image}…',
    'sandbox.imageFailed': 'Sandbox image {image} could not be prepared',
    'sandbox.imagePrepareFailed': 'Sandbox image preparation failed',

    // ─── terminal/* — shell panel + its WebSocket ────────────────────────────────
    // Only what a human reads. ANSI escapes, the `resize`/`input`/`output` frame types and the
    // key names a hint chip prints stay exactly as written.
    'terminal.connecting': 'Connecting...',
    'terminal.title': 'Terminal',
    'terminal.close': 'Close terminal',
    // The shortcut hint translates; the element's aria-label stays the plain English name, and the
    // virtual keyboard's key names (Escape, Arrow up…) are key names, not prose.
    'terminal.closeWithShortcut': 'Close terminal ({shortcut})',
    'terminal.activePortMappings': 'Active port mappings (Host : Container)',
    'terminal.copy': 'Copy',
    'terminal.paste': 'Paste',
    'terminal.noProject': 'No project selected',

    // ─── stats/* — project token-usage modal ─────────────────────────────────────────
    // Units (t/s, kWh, Wh, mWh) and number formatting belong to the formatting utils, not here.
    'stats.title': 'Token Usage',
    'stats.loading': 'Loading stats...',
    'stats.empty': 'No token usage data yet. Run a task to see stats here.',
    'stats.tasks': 'Tasks',
    'stats.cached': 'Cached',
    'stats.prompt': 'Prompt',
    'stats.generated': 'Generated',
    'stats.promptSpeed': 'Prompt Speed',
    'stats.genSpeed': 'Gen Speed',
    'stats.power': 'Power',
    'stats.estTotalCost': 'Est. Total Cost',
    'stats.costTitle': 'Includes API pricing and electricity cost (when configured)',
    'stats.model': 'Model',
    'stats.reset': 'Reset',
    'stats.resetTitle': 'Reset stats for this model',
    'stats.resetModelAria': 'Reset stats for {name}',
    'stats.resetConfirm': 'Reset token stats for {name}?',

    // ─── boardDnd/* + attachments/* — the two remaining service strings ───────────────
    'boardDnd.cannotMoveToBacklog': 'Cannot move to Backlog: parent task is In Progress',
    // {error} is whatever the request threw (often the server's own sentence) — never translated.
    'boardDnd.moveFailed': 'Failed to move task: {error}',
    'attachments.couldNotRead': 'Could not read {name}',
    'attachments.tooLarge': { one: '{count} image too large (max {limit} MB)', other: '{count} images too large (max {limit} MB)' },
    'attachments.onlyN': 'Only {max} images per message',
} as const;

/** Every key the app may ask for. A typo is a compile error, not a blank string at runtime. */
export type MessageKey = keyof typeof en;

/** English is the catalog of record: any key missing from another locale falls back to it. */
export const enCatalog: Catalog = en;
