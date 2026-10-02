/**
 * Adds an entry to `cache`, evicting the oldest once it holds `max`. The bound counts entries rather
 * than bytes, because an entry is one unit of work the cache exists to avoid redoing.
 */
export function storeBounded<V>(cache: Map<string, V>, key: string, value: V, max: number): void {
    if (cache.size >= max) {
        cache.delete(cache.keys().next().value as string);
    }
    cache.set(key, value);
}
