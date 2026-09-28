// @vitest-environment jsdom
// The Escape guard is a table, so the thing that can go wrong is a table that stopped matching the
// stores. These tests read what the store instances actually declare and fail when the table and the
// stores disagree — in either direction.
import { describe, it, expect, afterEach } from 'vitest';
import { appStore, dialogStore, uiStore } from '$lib/stores';
import { PromiseSlot } from '$lib/stores/dialogs.svelte';
import { MODAL_FLAGS, isAnyModalOpen } from '$lib/utils/modal-state';

/**
 * Modal-shaped fields that own no Escape, named as `<store>.<field>`. `isAnyModalOpen` reads
 * `MODAL_FLAGS` alone, so this decline list has no other reader — which is why it lives here rather
 * than beside the table. A field in `MODAL_FLAGS` and a field here is a contradiction the coverage
 * case fails on; an entry naming no field is stale and fails too.
 */
const ESCAPE_INDIFFERENT_FIELDS: readonly string[] = [
    // The full task view is closed by Escape rather than protected from it.
    'uiStore.showCardDetail',
];

/** The store instances the table draws on. A new store with modals belongs here too. */
const STORES = { uiStore, appStore, dialogStore } as const;
type StoreName = keyof typeof STORES;

const key = (store: string, field: string) => `${store}.${field}`;
const covered = new Set(MODAL_FLAGS.map((flag) => key(flag.store, flag.field)));
const declined = new Set(ESCAPE_INDIFFERENT_FIELDS);

/**
 * The fields an instance declares. A `$state` class field is an accessor on the prototype and a plain
 * field is an own property, so both lists are read; a method is never state whatever it is named, and
 * every `show*` method would otherwise read like a modal.
 */
function declaredFields(store: object): string[] {
    const proto = Object.getPrototypeOf(store) as object;
    const names = [...Object.keys(store), ...Object.getOwnPropertyNames(proto)];
    const record = store as Record<string, unknown>;
    return [...new Set(names)].filter(
        (name) => name !== 'constructor' && typeof record[name] !== 'function',
    );
}

/** A field that reads like a modal: a `show*`/`*Dialog*` flag, or a slot a dialog answers. */
function isModalShaped(store: Record<string, unknown>, field: string): boolean {
    return /^show/.test(field) || field.includes('Dialog') || store[field] instanceof PromiseSlot;
}

function storeOf(name: string): Record<string, unknown> | null {
    const store: object | undefined = (STORES as Record<string, object | undefined>)[name];
    return store ? (store as Record<string, unknown>) : null;
}

/** Put a flag back to closed: the six booleans are written, each slot is emptied. */
function close(flag: Record<string, unknown>, field: string): void {
    const value = flag[field];
    if (value instanceof PromiseSlot) value.clear();
    else if (typeof value === 'boolean') flag[field] = false;
}

function closeEverything(): void {
    for (const flag of MODAL_FLAGS) {
        const store = storeOf(flag.store);
        if (store) close(store, flag.field);
    }
}

/**
 * How each flag in the table is raised. A dialog is asked for through the store method rather than
 * written into the slot, so what is proven is the pair a caller actually uses.
 */
const RAISE: Record<string, () => void> = {
    'uiStore.showCreateTask': () => { uiStore.showCreateTask = true; },
    'uiStore.showConfig': () => { uiStore.showConfig = true; },
    'uiStore.showTerminal': () => { uiStore.showTerminal = true; },
    'uiStore.showStats': () => { uiStore.showStats = true; },
    'uiStore.showSandboxConfig': () => { uiStore.showSandboxConfig = true; },
    'appStore.showEditTask': () => { appStore.showEditTask = true; },
    'dialogStore.confirmationSlot': () => { void dialogStore.showConfirm({ title: 'T', message: 'M' }); },
    'dialogStore.commitDialogSlot': () => { void dialogStore.showCommitDialog({ message: 'm' }); },
    'dialogStore.compressContextDialogSlot': () => { void dialogStore.showCompressContextDialog({}); },
};

afterEach(closeEverything);

describe('the Escape-owning modal table', () => {
    it('names every modal-shaped field the stores declare', () => {
        const unclassified = (Object.keys(STORES) as StoreName[]).flatMap((name) => {
            const store = storeOf(name)!;
            return declaredFields(store)
                .filter((field) => isModalShaped(store, field))
                .filter((field) => !covered.has(key(name, field)) && !declined.has(key(name, field)))
                .map((field) => key(name, field));
        });
        expect(unclassified).toEqual([]);
    });

    it('points at a field each entry still has', () => {
        const staleTable = MODAL_FLAGS
            .filter((flag) => !declaredFields(storeOf(flag.store) ?? {}).includes(flag.field))
            .map((flag) => key(flag.store, flag.field));
        const staleDeclines = ESCAPE_INDIFFERENT_FIELDS.filter((entry) => {
            const [storeName, field] = entry.split('.');
            const store = storeOf(storeName);
            return !store || !field || !declaredFields(store).includes(field);
        });
        expect({ staleTable, staleDeclines }).toEqual({ staleTable: [], staleDeclines: [] });
    });

    it('declines no field it also covers', () => {
        const both = MODAL_FLAGS
            .filter((flag) => declined.has(key(flag.store, flag.field)))
            .map((flag) => key(flag.store, flag.field));
        expect(both).toEqual([]);
    });

    it('holds Escape for every flag the table names, and only those', () => {
        expect(isAnyModalOpen()).toBe(false);
        for (const flag of MODAL_FLAGS) {
            const name = key(flag.store, flag.field);
            const raise = RAISE[name];
            expect(raise, `${name} needs a way to be raised here`).toBeTypeOf('function');
            raise?.();
            expect(isAnyModalOpen(), name).toBe(true);
            closeEverything();
        }
        expect(isAnyModalOpen()).toBe(false);
    });

    it('leaves Escape to the app for every declined field', () => {
        for (const entry of ESCAPE_INDIFFERENT_FIELDS) {
            const [storeName, field] = entry.split('.');
            const store = storeOf(storeName);
            if (!store || !field || typeof store[field] !== 'boolean') continue;
            store[field] = true;
            expect(isAnyModalOpen(), entry).toBe(false);
            store[field] = false;
        }
    });
});
