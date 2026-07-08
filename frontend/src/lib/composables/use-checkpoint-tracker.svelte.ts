/**
 * Checkpoint tracker composable.
 * Manages pending checkpoints, commit labels, and commit hashes for the chat UI.
 *
 * Usage:
 *   const tracker = useCheckpointTracker();
 *   // Access: tracker.pendingCheckpoints, tracker.checkpointLabels, tracker.checkpointCommitHashes
 *   // Mutate: tracker.addPending(msgId), tracker.completeCheckpoint(msgId, label, hash)
 */
export function useCheckpointTracker() {
    let pendingCheckpoints = $state(new Set<string>());
    let checkpointLabels = $state(new Map<string, string>());
    let checkpointCommitHashes = $state(new Map<string, string>());
    // Bumper to force $derived re-evaluation when checkpoint maps change
    let checkpointHashesVersion = $state(0);

    function addPending(messageId: string) {
        const next = new Set(pendingCheckpoints);
        next.add(messageId);
        pendingCheckpoints = next;
    }

    function completeCheckpoint(messageId: string, label?: string, hash?: string) {
        const nextPending = new Set(pendingCheckpoints);
        nextPending.delete(messageId);
        pendingCheckpoints = nextPending;

        if (label) {
            const nextLabels = new Map(checkpointLabels);
            nextLabels.set(messageId, label);
            checkpointLabels = nextLabels;
        }
        if (hash) {
            const nextHashes = new Map(checkpointCommitHashes);
            nextHashes.set(messageId, hash);
            checkpointCommitHashes = nextHashes;
            checkpointHashesVersion++;
        }
    }

    function clearCheckpointState() {
        pendingCheckpoints = new Set();
        checkpointLabels = new Map();
        checkpointCommitHashes = new Map();
        checkpointHashesVersion++;
    }

    function restoreHashes(hashMap: Map<string, string>) {
        checkpointCommitHashes = new Map(hashMap);
        checkpointHashesVersion++;
    }

    return {
        pendingCheckpoints,
        checkpointLabels,
        checkpointCommitHashes,
        checkpointHashesVersion,
        addPending,
        completeCheckpoint,
        clearCheckpointState,
        restoreHashes,
    };
}
