/** Inline word-level diff segment for highlighting changes within a line. */
export interface InlineDiffSegment {
    key: string;
    text: string;
    tone: 'added' | 'removed' | 'context';
}

/** A single line in a parsed diff. */
export interface DiffRow {
    /** Unique key for rendering (optional, index-based fallback). */
    key?: string;
    /** Line number in the new file (or old file for removed lines), or null for headers. */
    lineNumber: number | null;
    /** Type of this line. */
    variant: 'context' | 'added' | 'removed';
    /** The text content (without the +/-/prefix). Omitted when segments present. */
    text?: string;
    /** Word-level inline diff segments (set for paired removed/added rows). */
    segments?: InlineDiffSegment[];
}

/** A diff row as returned by the API (variant/tone are strings, not strict literals). */
export interface ApiDiffRow {
    key?: string;
    lineNumber: number | null;
    variant: string;
    text?: string;
    segments?: Array<{ key: string; text: string; tone: string }>;
}

/**
 * Which half of the diff panel a file came from: the working sections compare HEAD against the files on
 * disk, the committed section compares two commits.
 */
export type DiffImageSection = 'working' | 'committed';

/** The commits one section reads a file from. `new` omitted means the file as it stands on disk. */
interface DiffImageRefs {
    old?: string;
    new?: string;
}

/** Which project owns a binary file, and what each section of the panel compares. */
export interface DiffImageSource {
    project: string;
    task?: string;
    working?: DiffImageRefs;
    committed?: DiffImageRefs;
}

/** One "View image" click, in the shape ImageDiffModal and GET /api/fs/raw want it. */
export interface DiffImageRequest {
    path: string;
    displayPath: string;
    oldPath: string;
    oldRef: string;
    newRef: string;
}

/** One "List contents" click, in the shape ArchiveModal and GET /api/fs/archive want it. */
export interface DiffArchiveRequest {
    path: string;
    /** What the row is called where it was clicked, rename included. */
    displayPath: string;
    /** The side of the comparison to read; empty is the file on disk. */
    ref: string;
}

/**
 * The manifest body `/tasks/{id}/diff` answers with, and the body the `diff_changed` frame carries —
 * one shape over two transports (`api.diffManifest` and `Server.broadcastTaskDiff` in Go). Every field is
 * optional because the route answers differently per query: the per-file manifest here, or the patch text
 * in `TaskDiff`'s own fields, and a cached body may hold only one of the two.
 */
export interface DiffManifest {
    /** Per-file metadata keyed by path. Never the patch text: rows come from `/diff/file`. */
    files?: Record<string, DiffManifestEntry>;
    short_stat?: string;
    /** The ref the diff was taken against, so a later comparison can name it. */
    base_ref?: string;
}

/**
 * One entry of that manifest — `api.diffManifestFile` in Go. The names are its JSON tags, and the pair of
 * them is the whole contract: the manifest says which files changed and how big the change is, never the
 * patch text, which arrives per file from `/diff/file`.
 *
 * Declared once because three browser copies of it disagreed: `staged` and `is_binary` were visible to the
 * diff cache and invisible to the API layer, so a field the server sent was a field the type said could not
 * arrive.
 */
interface DiffManifestEntry {
    hash: string;
    /** `tracked` or `untracked` — which half of the worktree the entry describes. */
    type: string;
    added?: number;
    removed?: number;
    truncated?: boolean;
    origAdded?: number;
    origRemoved?: number;
    /** Total line count, set for an untracked file whose lines were omitted. */
    line_count?: number;
    file_size?: number;
    is_binary?: boolean;
    /** True for a file the change created or removed. */
    isNew?: boolean;
    isDeleted?: boolean;
    /** True while the path is staged in the index. */
    staged?: boolean;
}

/**
 * One file, read back from `/diff/file` — the rows the panel renders plus the stats that decide how to
 * label them. `committedRows` is the committed half of the same file, present when the panel shows
 * working and committed changes side by side.
 *
 * The names follow `git.TrackedDiffFile`, which spells them `fileSize`/`isBinary` — unlike the manifest
 * entry above, which is a different Go struct and spells them `file_size`/`is_binary`. Both are pinned
 * to their struct: renaming either one silently drops the value.
 */
export interface DiffFileResponse {
    type?: string;
    lines?: string[];
    line_count?: number;
    rows?: ApiDiffRow[];
    committedRows?: ApiDiffRow[];
    truncated?: boolean;
    origAdded?: number;
    origRemoved?: number;
    isBinary?: boolean;
    fileSize?: number;
    isNew?: boolean;
    isDeleted?: boolean;
}

/** A parsed file group from a unified diff. */
export interface DiffFile {
    /** File path (e.g., "src/lib/utils.ts"). */
    path: string;
    /** Original path for renames (e.g., "old-name.png" when path is "new-name.png"). */
    origPath?: string;
    /** Diff rows for this file. Empty for binary/large files. */
    rows: DiffRow[];
    /** Diff rows committed to branch since baseRef. */
    committedRows?: DiffRow[];
    /** Count of added lines. */
    added: number;
    /** Count of removed lines. */
    removed: number;
    /** File size in bytes, set when file is binary or too large to diff. */
    fileSize?: number;
    /** True if git detected this as a binary file. */
    isBinary?: boolean;
    /** True if this file was truncated (has more changes not shown). */
    truncated?: boolean;
    /** Original added count before truncation (for display in header). */
    origAdded?: number;
    /** Original removed count before truncation (for display in header). */
    origRemoved?: number;
    /** True if this file is staged in the git index. */
    staged?: boolean;
    /** True if this file was deleted (git diff shows +++ /dev/null). */
    isDeleted?: boolean;
    /** True if this file is newly added (git diff shows --- /dev/null). */
    isNewFile?: boolean;
}
