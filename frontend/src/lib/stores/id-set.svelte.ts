/**
 * A set of ids that a reactive reader can depend on. Every change replaces the `Set`, because
 * Svelte sees the new reference and not an in-place edit; a reader takes `all` and never mutates
 * it. A subclass adds whatever its domain means by membership — nothing here knows what an id is.
 */
export class IdSet {
    protected ids = $state<Set<string>>(new Set());

    /** The set as it stands. Mutate through the methods. */
    get all(): Set<string> {
        return this.ids;
    }

    has(id: string): boolean {
        return this.ids.has(id);
    }

    add(id: string): void {
        if (this.ids.has(id)) return;
        const next = new Set(this.ids);
        next.add(id);
        this.ids = next;
    }

    delete(id: string): void {
        if (!this.ids.has(id)) return;
        const next = new Set(this.ids);
        next.delete(id);
        this.ids = next;
    }
}
