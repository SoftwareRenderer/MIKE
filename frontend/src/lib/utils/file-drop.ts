/**
 * Turning a drop event into a list of files with the paths they were dropped with.
 *
 * `DataTransfer.files` flattens a folder drop to the folder's name, so the tree comes
 * from the (non-standard but universal) `webkitGetAsEntry` API and the paths are
 * rebuilt from the entry chain. Where that API is absent the flat file list is used:
 * a single-file drop still works, only the folder structure is lost.
 */

/** Byte cap for one uploaded file; it mirrors MaxUploadSize on the server. */
export const MAX_UPLOAD_BYTES = 20 * 1024 * 1024;

/**
 * Ceiling on one drop. Not a quota but a stop-loss: `node_modules` is a drag of tens
 * of thousands of files, and a drop that size would fill the project with files nobody
 * meant to commit. Truncation is reported, never uploaded silently.
 */
export const MAX_DROP_FILES = 1000;

/** One file of a drop, named relative to the directory it was dropped into. */export interface DroppedFile {
    file: File;
    /** '/'-separated path inside the target directory, e.g. `assets/img/logo.png`. */
    relPath: string;
}

/** What a single drop event turned into. */
export interface DroppedSelection {
    files: DroppedFile[];
    /** Files over MAX_UPLOAD_BYTES — reported, never sent. */
    tooBig: DroppedFile[];
    /** Names that could not be read (a link, a permission refusal, a vanished file). */
    unreadable: string[];
    /** The drop was still filling when MAX_DROP_FILES was reached. */
    truncated: boolean;
}

// The entry API is not in TypeScript's DOM lib (it is the pre-FileSystemHandle shape
// only drops use), so the parts we call are declared here.
interface EntryFile {
    readonly isFile: true;
    readonly isDirectory: false;
    readonly name: string;
    file(success: (file: File) => void, error: (err: unknown) => void): void;
}

interface EntryReader {
    readEntries(success: (entries: Entry[]) => void, error: (err: unknown) => void): void;
}

interface EntryDirectory {
    readonly isFile: false;
    readonly isDirectory: true;
    readonly name: string;
    createReader(): EntryReader;
}

type Entry = EntryFile | EntryDirectory;

interface GetAsEntryItem {
    webkitGetAsEntry?(): Entry | null;
}

/** The drop has room for no more files. */
function atCap(out: DroppedSelection): boolean {
    return out.files.length + out.tooBig.length >= MAX_DROP_FILES;
}

/** Sort one file into the run or the oversize list — the same rule for every source. */
function classify(out: DroppedSelection, file: File, relPath: string): void {
    (file.size > MAX_UPLOAD_BYTES ? out.tooBig : out.files).push({ file, relPath });
}

/** Whether a drag carries files at all — an in-app drag (cards, selected text) must not light up the drop zone. */
export function dragCarriesFiles(dataTransfer: DataTransfer | null | undefined): boolean {
    if (!dataTransfer) return false;
    return Array.from(dataTransfer.types).includes('Files');
}

function readEntryFile(entry: EntryFile): Promise<File | null> {
    return new Promise((resolve) => {
        entry.file(
            (file) => resolve(file),
            () => resolve(null),
        );
    });
}

/**
 * One batch of a directory's children. `readEntries` hands out a bounded batch per call
 * (Chromium: 100) and an empty batch means the directory is spent — which is why the
 * reader is created once per directory: a fresh reader restarts at the first entry, so
 * re-creating it would re-read the same hundred forever.
 */
function readBatch(reader: EntryReader): Promise<Entry[] | null> {
    return new Promise((resolve) => {
        reader.readEntries(
            (entries) => resolve(entries),
            () => resolve(null),
        );
    });
}

async function walk(entry: Entry, prefix: string, out: DroppedSelection): Promise<void> {
    const relPath = prefix ? `${prefix}/${entry.name}` : entry.name;
    if (atCap(out)) {
        out.truncated = true;
        return;
    }
    if (entry.isDirectory) {
        const reader = entry.createReader();
        for (;;) {
            const batch = await readBatch(reader);
            if (batch === null) {
                out.unreadable.push(relPath);
                return;
            }
            if (batch.length === 0) break;
            for (const child of batch) {
                await walk(child, relPath, out);
                if (out.truncated) return;
            }
        }
        return;
    }
    const file = await readEntryFile(entry);
    if (!file) {
        out.unreadable.push(relPath);
        return;
    }
    classify(out, file, relPath);
}

/**
 * Collect everything a drop event carries, keeping the folder structure the user
 * dragged. Never throws: what cannot be read is reported in `unreadable`.
 */
export async function collectDroppedFiles(dataTransfer: DataTransfer): Promise<DroppedSelection> {
    const out: DroppedSelection = { files: [], tooBig: [], unreadable: [], truncated: false };
    const items = Array.from(dataTransfer.items || []);
    const entries: Entry[] = [];
    for (const item of items) {
        if (item.kind !== 'file') continue;
        const entry = (item as unknown as GetAsEntryItem).webkitGetAsEntry?.();
        if (entry) entries.push(entry);
    }

    if (entries.length === 0) {
        // No entry API: the flat list is all this browser described, and a bare name is
        // a complete path for a single file.
        for (const file of Array.from(dataTransfer.files || [])) {
            if (atCap(out)) {
                out.truncated = true;
                break;
            }
            classify(out, file, file.name);
        }
        return out;
    }

    for (const entry of entries) {
        if (out.truncated) break;
        await walk(entry, '', out);
    }
    return out;
}

/**
 * Files chosen through an `<input type="file">`, in the shape a drop produces. A picker
 * reports bare names, so these land flat — carrying a folder's tree is the drag's job.
 */
export function collectPickedFiles(files: File[]): DroppedSelection {
    const out: DroppedSelection = { files: [], tooBig: [], unreadable: [], truncated: false };
    for (const file of files) {
        if (atCap(out)) {
            out.truncated = true;
            break;
        }
        classify(out, file, file.name);
    }
    return out;
}
