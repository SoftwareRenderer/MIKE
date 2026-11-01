import type { StorageUsage } from '$lib/types';

/** The server's filesystem, as the app stream last pushed it; `null` until a frame arrives, never
 *  persisted and never fetched. docs/api-contract.md → The filesystem reading is pushed */
class StorageStatusStore {
    usage = $state<StorageUsage | null>(null);

    update(usage: StorageUsage): void {
        this.usage = usage;
    }
}

export const storageStatus = new StorageStatusStore();
