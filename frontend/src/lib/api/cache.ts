// =============================================================================
// Cache marker utility
// =============================================================================
// Marks data served from the offline board cache so the UI can display
// a "Viewing cached data" banner. Shared by all modules that read from
// the board cache (projects, config, etc.).
// =============================================================================

/** Runtime marker on cached data to indicate it was served from the offline mirror. */
const CACHE_MARKER = Symbol('fromCache');

/** Check if an object was served from the offline cache. */
export function isFromCache(obj: unknown): boolean {
    return (obj as any)?.[CACHE_MARKER] === true;
}

/** Mark data as served from cache. Mutates and returns the object. */
export function markCached<T extends object>(data: T): T {
    return Object.assign(data, { [CACHE_MARKER]: true }) as T;
}

/** The fetch never reached the server, so a cached copy is the only thing worth showing.
 * Narrower than queue.ts's isTransientError, which also counts apiFetch's own 15 s abort: that one
 * means "ask again", and answering it from cache hides a slow server behind an older copy. */
export function isDisconnectedError(e: unknown): boolean {
    return (e as any)?.name === 'TypeError';
}
