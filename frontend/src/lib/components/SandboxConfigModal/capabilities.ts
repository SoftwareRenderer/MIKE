/**
 * The capability names the Sandbox Configuration offers as suggestions. They are Linux capability
 * constants — the machine value a container is granted — so the NAME never translates; only the
 * one-line description beside it does (`sandbox.cap.<NAME>`).
 *
 * What may be granted is decided by `sandboxruntime.ValidateCapabilities` on the Go side, which
 * judges each name by shape and applies the isolation posture first; this list is only what the
 * input offers, and typing a name not on it is still a name the server rules on.
 *
 * @knipignore The suggestion tests walk the whole list; `capSuggestionsFor` answers eight at a time.
 */
export const KNOWN_CAPABILITIES: readonly string[] = [
    'CAP_CHOWN',
    'CAP_DAC_OVERRIDE',
    'CAP_DAC_READ_SEARCH',
    'CAP_FOWNER',
    'CAP_FSETID',
    'CAP_IPC_LOCK',
    'CAP_IPC_OWNER',
    'CAP_KILL',
    'CAP_LEASE',
    'CAP_LINUX_IMMUTABLE',
    'CAP_MKNOD',
    'CAP_NET_ADMIN',
    'CAP_NET_BIND_SERVICE',
    'CAP_NET_BROADCAST',
    'CAP_NET_RAW',
    'CAP_SETFCAP',
    'CAP_SETGID',
    'CAP_SETPCAP',
    'CAP_SETUID',
    'CAP_SYS_CHROOT',
    'CAP_SYS_PTRACE',
    'CAP_SYS_ADMIN',
    'CAP_SYS_BOOT',
    'CAP_SYS_MODULE',
    'CAP_SYS_NICE',
    'CAP_SYS_RAWIO',
    'CAP_SYS_RESOURCE',
    'CAP_SYS_TIME',
    'CAP_SYS_TTY_CONFIG',
    'CAP_BPF',
    'CAP_PERFMON',
    'CAP_AUDIT_CONTROL',
    'CAP_AUDIT_READ',
    'CAP_AUDIT_WRITE',
    'CAP_BLOCK_SUSPEND',
    'CAP_CHECKPOINT_RESTORE',
    'CAP_MAC_ADMIN',
    'CAP_MAC_OVERRIDE',
    'CAP_WAKE_ALARM',
    'CAP_SYSLOG',
];

/**
 * Docker's documented default set. podman defaults to 11 of these and a host may replace them in
 * `containers.conf`, so this names Docker's set rather than asking the host what it defaults to.
 *
 * @knipignore Its tests measure the set and its order; the app reaches it through `capabilitiesToAdd`.
 */
export const DOCKER_DEFAULT_CAPABILITIES: readonly string[] = [
    'CAP_CHOWN',
    'CAP_DAC_OVERRIDE',
    'CAP_FSETID',
    'CAP_FOWNER',
    'CAP_MKNOD',
    'CAP_NET_RAW',
    'CAP_SETGID',
    'CAP_SETUID',
    'CAP_SETFCAP',
    'CAP_SETPCAP',
    'CAP_NET_BIND_SERVICE',
    'CAP_SYS_CHROOT',
    'CAP_KILL',
    'CAP_AUDIT_WRITE',
];

/** The defaults not already granted. */
export function capabilitiesToAdd(selected: readonly string[]): string[] {
    return DOCKER_DEFAULT_CAPABILITIES.filter(cap => !selected.includes(cap));
}

/** The field's text is a capability name whatever case it was typed in. */
export function normalizeCapability(cap: string): string {
    return cap.trim().toUpperCase();
}

/** Suggestions for a partial name: not already granted, matching, at most a dropdown's worth. */
export function capSuggestionsFor(query: string, selected: readonly string[]): string[] {
    const upper = query.trim().toUpperCase();
    if (!upper) return [];
    return KNOWN_CAPABILITIES.filter(cap =>
        !selected.includes(cap) && cap.toUpperCase().includes(upper)
    ).slice(0, 8);
}
